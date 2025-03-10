// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// The Tagger is the central source of truth for client-side entity tagging.
// It subscribes to workloadmeta to get updates for all the entity kinds
// (containers, kubernetes pods, kubernetes nodes, etc.) and extracts the tags for each of them.
// Tags are then stored in memory (by the TagStore) and can be queried by the tagger.Tag()
// method.

// Package taggerimpl contains the implementation of the tagger component.
package taggerimpl

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	api "github.com/DataDog/datadog-agent/comp/api/api/def"
	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	taggermock "github.com/DataDog/datadog-agent/comp/core/tagger/mock"
	"github.com/DataDog/datadog-agent/comp/core/tagger/origindetection"
	"github.com/DataDog/datadog-agent/comp/core/tagger/telemetry"
	"github.com/DataDog/datadog-agent/comp/core/tagger/types"
	"github.com/DataDog/datadog-agent/comp/core/tagger/utils"
	coretelemetry "github.com/DataDog/datadog-agent/comp/core/telemetry"
	workloadmeta "github.com/DataDog/datadog-agent/comp/core/workloadmeta/def"
	compdef "github.com/DataDog/datadog-agent/comp/def"
	"github.com/DataDog/datadog-agent/comp/dogstatsd/packets"
	taggertypes "github.com/DataDog/datadog-agent/pkg/tagger/types"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/util/common"
	"github.com/DataDog/datadog-agent/pkg/util/containers/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/containers/metrics/provider"
	httputils "github.com/DataDog/datadog-agent/pkg/util/http"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

// Requires defines the dependencies of the tagger component.
type Requires struct {
	compdef.In

	Lc        compdef.Lifecycle
	Config    config.Component
	Log       log.Component
	Wmeta     workloadmeta.Component
	Telemetry coretelemetry.Component
	Params    tagger.Params
}

// Provides contains the fields provided by the tagger constructor.
type Provides struct {
	compdef.Out

	Comp     tagger.Component
	Endpoint api.AgentEndpointProvider
}

// datadogConfig contains the configuration specific to Dogstatsd.
type datadogConfig struct {
	// dogstatsdEntityIDPrecedenceEnabled disable enriching Dogstatsd metrics with tags from "origin detection" when Entity-ID is set.
	dogstatsdEntityIDPrecedenceEnabled bool
	// dogstatsdOptOutEnabled If enabled, and cardinality is none no origin detection is performed.
	dogstatsdOptOutEnabled bool
	// originDetectionUnifiedEnabled If enabled, all origin detection mechanisms will be unified to use the same logic.
	originDetectionUnifiedEnabled bool
}

// TaggerWrapper is a struct that contains two tagger component: capturetagger and the local tagger
// and implements the tagger interface
type TaggerWrapper struct {
	defaultTagger tagger.Component

	wmeta         workloadmeta.Component
	datadogConfig datadogConfig

	checksCardinality          types.TagCardinality
	dogstatsdCardinality       types.TagCardinality
	tlmUDPOriginDetectionError coretelemetry.Counter
	telemetryStore             *telemetry.Store

	log log.Component
}

// NewComponent returns a new tagger client
func NewComponent(req Requires) (Provides, error) {
	taggerClient, err := NewTaggerClient(req.Params, req.Config, req.Wmeta, req.Log, req.Telemetry)

	if err != nil {
		return Provides{}, err
	}

	taggerClient.wmeta = req.Wmeta

	req.Log.Info("TaggerClient is created, defaultTagger type: ", reflect.TypeOf(taggerClient.defaultTagger))
	req.Lc.Append(compdef.Hook{OnStart: func(_ context.Context) error {
		// Main context passed to components, consistent with the one used in the workloadmeta component
		mainCtx, _ := common.GetMainCtxCancel()
		return taggerClient.Start(mainCtx)
	}})
	req.Lc.Append(compdef.Hook{OnStop: func(context.Context) error {
		return taggerClient.Stop()
	}})

	return Provides{
		Comp:     taggerClient,
		Endpoint: api.NewAgentEndpointProvider(taggerClient.writeList, "/tagger-list", "GET"),
	}, nil
}

// NewTaggerClient returns a new tagger client
func NewTaggerClient(params tagger.Params, cfg config.Component, wmeta workloadmeta.Component, log log.Component, telemetryComp coretelemetry.Component) (*TaggerWrapper, error) {
	var defaultTagger tagger.Component
	var err error
	telemetryStore := telemetry.NewStore(telemetryComp)
	if params.UseFakeTagger {
		defaultTagger = taggermock.New().Comp
	} else {
		defaultTagger, err = newLocalTagger(cfg, wmeta, log, telemetryStore)
	}

	if err != nil {
		return nil, err
	}

	wrapper := &TaggerWrapper{
		defaultTagger:  defaultTagger,
		log:            log,
		telemetryStore: telemetryStore,
	}

	checkCard := cfg.GetString("checks_tag_cardinality")
	dsdCard := cfg.GetString("dogstatsd_tag_cardinality")
	wrapper.checksCardinality, err = types.StringToTagCardinality(checkCard)
	if err != nil {
		log.Warnf("failed to parse check tag cardinality, defaulting to low. Error: %s", err)
		wrapper.checksCardinality = types.LowCardinality
	}

	wrapper.dogstatsdCardinality, err = types.StringToTagCardinality(dsdCard)
	if err != nil {
		log.Warnf("failed to parse dogstatsd tag cardinality, defaulting to low. Error: %s", err)
		wrapper.dogstatsdCardinality = types.LowCardinality
	}

	wrapper.datadogConfig.dogstatsdEntityIDPrecedenceEnabled = cfg.GetBool("dogstatsd_entity_id_precedence")
	wrapper.datadogConfig.originDetectionUnifiedEnabled = cfg.GetBool("origin_detection_unified")
	wrapper.datadogConfig.dogstatsdOptOutEnabled = cfg.GetBool("dogstatsd_origin_optout_enabled")
	// we use to pull tagger metrics in dogstatsd. Pulling it later in the
	// pipeline improve memory allocation. We kept the old name to be
	// backward compatible and because origin detection only affect
	// dogstatsd metrics.
	wrapper.tlmUDPOriginDetectionError = telemetryComp.NewCounter("dogstatsd", "udp_origin_detection_error", nil, "Dogstatsd UDP origin detection error count")

	return wrapper, nil
}

func (t *TaggerWrapper) writeList(w http.ResponseWriter, _ *http.Request) {
	response := t.List()

	jsonTags, err := json.Marshal(response)
	if err != nil {
		httputils.SetJSONError(w, t.log.Errorf("Unable to marshal tagger list response: %s", err), 500)
		return
	}
	w.Write(jsonTags)
}

// Start calls defaultTagger.Start
func (t *TaggerWrapper) Start(ctx context.Context) error {
	return t.defaultTagger.Start(ctx)
}

// Stop calls defaultTagger.Stop
func (t *TaggerWrapper) Stop() error {
	return t.defaultTagger.Stop()
}

// GetTaggerTelemetryStore returns tagger telemetry store
func (t *TaggerWrapper) GetTaggerTelemetryStore() *telemetry.Store {
	return t.telemetryStore
}

// GetDefaultTagger returns the default Tagger in current instance
func (t *TaggerWrapper) GetDefaultTagger() tagger.Component {
	return t.defaultTagger
}

// GetEntity returns the hash for the provided entity id.
func (t *TaggerWrapper) GetEntity(entityID types.EntityID) (*types.Entity, error) {
	return t.defaultTagger.GetEntity(entityID)
}

// Tag queries the captureTagger (for replay scenarios) or the defaultTagger.
// It can return tags at high cardinality (with tags about individual containers),
// or at orchestrator cardinality (pod/task level).
func (t *TaggerWrapper) Tag(entityID types.EntityID, cardinality types.TagCardinality) ([]string, error) {
	return t.defaultTagger.Tag(entityID, cardinality)
}

// LegacyTag has the same behaviour as the Tag method, but it receives the entity id as a string and parses it.
// If possible, avoid using this function, and use the Tag method instead.
// This function exists in order not to break backward compatibility with rtloader and python
// integrations using the tagger
func (t *TaggerWrapper) LegacyTag(entity string, cardinality types.TagCardinality) ([]string, error) {
	prefix, id, err := types.ExtractPrefixAndID(entity)
	if err != nil {
		return nil, err
	}

	entityID := types.NewEntityID(prefix, id)
	return t.Tag(entityID, cardinality)
}

// AccumulateTagsFor queries the defaultTagger to get entity tags from cache or
// sources and appends them to the TagsAccumulator.  It can return tags at high
// cardinality (with tags about individual containers), or at orchestrator
// cardinality (pod/task level).
func (t *TaggerWrapper) AccumulateTagsFor(entityID types.EntityID, cardinality types.TagCardinality, tb tagset.TagsAccumulator) error {
	return t.defaultTagger.AccumulateTagsFor(entityID, cardinality, tb)
}

// GetEntityHash returns the hash for the tags associated with the given entity
// Returns an empty string if the tags lookup fails
func (t *TaggerWrapper) GetEntityHash(entityID types.EntityID, cardinality types.TagCardinality) string {
	tags, err := t.Tag(entityID, cardinality)
	if err != nil {
		return ""
	}
	return utils.ComputeTagsHash(tags)
}

// Standard queries the defaultTagger to get entity
// standard tags (env, version, service) from cache or sources.
func (t *TaggerWrapper) Standard(entityID types.EntityID) ([]string, error) {
	return t.defaultTagger.Standard(entityID)
}

// AgentTags returns the agent tags
// It relies on the container provider utils to get the Agent container ID
func (t *TaggerWrapper) AgentTags(cardinality types.TagCardinality) ([]string, error) {
	ctrID, err := metrics.GetProvider(option.New(t.wmeta)).GetMetaCollector().GetSelfContainerID()
	if err != nil {
		return nil, err
	}

	if ctrID == "" {
		return nil, nil
	}

	entityID := types.NewEntityID(types.ContainerID, ctrID)
	return t.Tag(entityID, cardinality)
}

// GlobalTags queries global tags that should apply to all data coming from the
// agent.
func (t *TaggerWrapper) GlobalTags(cardinality types.TagCardinality) ([]string, error) {
	return t.defaultTagger.Tag(types.GetGlobalEntityID(), cardinality)
}

// globalTagBuilder queries global tags that should apply to all data coming
// from the agent and appends them to the TagsAccumulator
func (t *TaggerWrapper) globalTagBuilder(cardinality types.TagCardinality, tb tagset.TagsAccumulator) error {
	return t.defaultTagger.AccumulateTagsFor(types.GetGlobalEntityID(), cardinality, tb)
}

// List the content of the defaulTagger
func (t *TaggerWrapper) List() types.TaggerListResponse {
	return t.defaultTagger.List()
}

// EnrichTags extends a tag list with origin detection tags
// NOTE(remy): it is not needed to sort/dedup the tags anymore since after the
// enrichment, the metric and its tags is sent to the context key generator, which
// is taking care of deduping the tags while generating the context key.
// This function is dupliacted in the remote tagger `impl-remote`.
// When modifying this function make sure to update the copy `impl-remote` as well.
// TODO: extract this function to a share function so it can be used in both implementations
func (t *TaggerWrapper) EnrichTags(tb tagset.TagsAccumulator, originInfo taggertypes.OriginInfo) {
	cardinality := taggerCardinality(originInfo.Cardinality, t.dogstatsdCardinality, t.log)

	productOrigin := originInfo.ProductOrigin
	// If origin_detection_unified is disabled, we use DogStatsD's Legacy Origin Detection.
	// TODO: remove this when origin_detection_unified is enabled by default
	if !t.datadogConfig.originDetectionUnifiedEnabled && productOrigin == origindetection.ProductOriginDogStatsD {
		productOrigin = origindetection.ProductOriginDogStatsDLegacy
	}

	containerIDFromSocketCutIndex := len(types.ContainerID) + types.GetSeparatorLengh()

	// Generate container ID from Inode
	if originInfo.LocalData.ContainerID == "" {
		var inodeResolutionError error
		originInfo.LocalData.ContainerID, inodeResolutionError = t.generateContainerIDFromInode(originInfo.LocalData, metrics.GetProvider(option.New(t.wmeta)).GetMetaCollector())
		if inodeResolutionError != nil {
			t.log.Tracef("Failed to resolve container ID from inode %d: %v", originInfo.LocalData.Inode, inodeResolutionError)
		}
	}

	switch productOrigin {
	case origindetection.ProductOriginDogStatsDLegacy:
		// The following was moved from the dogstatsd package
		// originFromUDS is the origin discovered via UDS origin detection (container ID).
		// originFromTag is the origin sent by the client via the dd.internal.entity_id tag (non-prefixed pod uid).
		// originFromMsg is the origin sent by the client via the container field (non-prefixed container ID).
		// entityIDPrecedenceEnabled refers to the dogstatsd_entity_id_precedence parameter.
		//
		//	---------------------------------------------------------------------------------
		//
		// | originFromUDS | originFromTag | entityIDPrecedenceEnabled || Result: udsOrigin  |
		// |---------------|---------------|---------------------------||--------------------|
		// | any           | any           | false                     || originFromUDS      |
		// | any           | any           | true                      || empty              |
		// | any           | empty         | any                       || originFromUDS      |
		//
		//	---------------------------------------------------------------------------------
		//
		//	---------------------------------------------------------------------------------
		//
		// | originFromTag          | originFromMsg   || Result: originFromClient            |
		// |------------------------|-----------------||-------------------------------------|
		// | not empty && not none  | any             || pod prefix + originFromTag          |
		// | empty                  | empty           || empty                               |
		// | none                   | empty           || empty                               |
		// | empty                  | not empty       || container prefix + originFromMsg    |
		// | none                   | not empty       || container prefix + originFromMsg    |
		if t.datadogConfig.dogstatsdOptOutEnabled && originInfo.Cardinality == types.NoneCardinalityString {
			originInfo.ContainerIDFromSocket = packets.NoOrigin
			originInfo.LocalData.PodUID = ""
			originInfo.LocalData.ContainerID = ""
			return
		}

		// We use the UDS socket origin if no origin ID was specify in the tags
		// or 'dogstatsd_entity_id_precedence' is set to False (default false).
		if originInfo.ContainerIDFromSocket != packets.NoOrigin &&
			(originInfo.LocalData.PodUID == "" || !t.datadogConfig.dogstatsdEntityIDPrecedenceEnabled) &&
			len(originInfo.ContainerIDFromSocket) > containerIDFromSocketCutIndex {
			containerID := originInfo.ContainerIDFromSocket[containerIDFromSocketCutIndex:]
			originFromClient := types.NewEntityID(types.ContainerID, containerID)
			if err := t.AccumulateTagsFor(originFromClient, cardinality, tb); err != nil {
				t.log.Errorf("%s", err.Error())
			}
		}

		// originFromClient can either be originInfo.FromTag or originInfo.FromMsg
		var originFromClient types.EntityID
		if originInfo.LocalData.PodUID != "" && originInfo.LocalData.PodUID != "none" {
			// Check if the value is not "none" in order to avoid calling the tagger for entity that doesn't exist.
			// Currently only supported for pods
			originFromClient = types.NewEntityID(types.KubernetesPodUID, originInfo.LocalData.PodUID)
		} else if originInfo.LocalData.PodUID == "" && len(originInfo.LocalData.ContainerID) > 0 {
			// originInfo.FromMsg is the container ID sent by the newer clients.
			originFromClient = types.NewEntityID(types.ContainerID, originInfo.LocalData.ContainerID)
		}

		if !originFromClient.Empty() {
			if err := t.AccumulateTagsFor(originFromClient, cardinality, tb); err != nil {
				t.tlmUDPOriginDetectionError.Inc()
				t.log.Tracef("Cannot get tags for entity %s: %s", originFromClient, err)
			}
		}
	default:
		// Disable origin detection if cardinality is none
		if originInfo.Cardinality == types.NoneCardinalityString {
			originInfo.ContainerIDFromSocket = packets.NoOrigin
			originInfo.LocalData.PodUID = ""
			originInfo.LocalData.ContainerID = ""
			return
		}

		// Tag using Local Data
		if originInfo.ContainerIDFromSocket != packets.NoOrigin && len(originInfo.ContainerIDFromSocket) > containerIDFromSocketCutIndex {
			containerID := originInfo.ContainerIDFromSocket[containerIDFromSocketCutIndex:]
			originFromClient := types.NewEntityID(types.ContainerID, containerID)
			if err := t.AccumulateTagsFor(originFromClient, cardinality, tb); err != nil {
				t.log.Errorf("%s", err.Error())
			}
		}

		if err := t.AccumulateTagsFor(types.NewEntityID(types.ContainerID, originInfo.LocalData.ContainerID), cardinality, tb); err != nil {
			t.log.Tracef("Cannot get tags for entity %s: %s", originInfo.LocalData.ContainerID, err)
		}

		if err := t.AccumulateTagsFor(types.NewEntityID(types.KubernetesPodUID, originInfo.LocalData.PodUID), cardinality, tb); err != nil {
			t.log.Tracef("Cannot get tags for entity %s: %s", originInfo.LocalData.PodUID, err)
		}

		// Accumulate tags for pod UID
		if originInfo.ExternalData.PodUID != "" {
			if err := t.AccumulateTagsFor(types.NewEntityID(types.KubernetesPodUID, originInfo.ExternalData.PodUID), cardinality, tb); err != nil {
				t.log.Tracef("Cannot get tags for entity %s: %s", originInfo.ExternalData.PodUID, err)
			}
		}

		// Generate container ID from External Data
		generatedContainerID, err := t.generateContainerIDFromExternalData(originInfo.ExternalData, metrics.GetProvider(option.New(t.wmeta)).GetMetaCollector())
		if err != nil {
			t.log.Tracef("Failed to generate container ID from %v: %s", originInfo.ExternalData, err)
		}

		// Accumulate tags for generated container ID
		if generatedContainerID != "" {
			if err := t.AccumulateTagsFor(types.NewEntityID(types.ContainerID, generatedContainerID), cardinality, tb); err != nil {
				t.log.Tracef("Cannot get tags for entity %s: %s", generatedContainerID, err)
			}
		}
	}

	if err := t.globalTagBuilder(cardinality, tb); err != nil {
		t.log.Error(err.Error())
	}
}

// GenerateContainerIDFromOriginInfo generates a container ID from Origin Info.
func (t *TaggerWrapper) GenerateContainerIDFromOriginInfo(originInfo origindetection.OriginInfo) (string, error) {
	return t.defaultTagger.GenerateContainerIDFromOriginInfo(originInfo)
}

// generateContainerIDFromInode generates a container ID from the CGroup inode.
func (t *TaggerWrapper) generateContainerIDFromInode(e origindetection.LocalData, metricsProvider provider.ContainerIDForInodeRetriever) (string, error) {
	return metricsProvider.GetContainerIDForInode(e.Inode, time.Second)
}

// generateContainerIDFromExternalData generates a container ID from the External Data.
func (t *TaggerWrapper) generateContainerIDFromExternalData(e origindetection.ExternalData, metricsProvider provider.ContainerIDForPodUIDAndContNameRetriever) (string, error) {
	return metricsProvider.ContainerIDForPodUIDAndContName(e.PodUID, e.ContainerName, e.Init, time.Second)
}

// ChecksCardinality defines the cardinality of tags we should send for check metrics
// this can still be overridden when calling get_tags in python checks.
func (t *TaggerWrapper) ChecksCardinality() types.TagCardinality {
	return t.checksCardinality
}

// taggerCardinality converts tagger cardinality string to types.TagCardinality
// It should be defaulted to DogstatsdCardinality if the string is empty or unknown
func taggerCardinality(cardinality string,
	defaultCardinality types.TagCardinality,
	l log.Component) types.TagCardinality {
	if cardinality == "" {
		return defaultCardinality
	}

	taggerCardinality, err := types.StringToTagCardinality(cardinality)
	if err != nil {
		l.Tracef("Couldn't convert cardinality tag: %v", err)
		return defaultCardinality
	}

	return taggerCardinality
}

// Subscribe calls defaultTagger.Subscribe
func (t *TaggerWrapper) Subscribe(subscriptionID string, filter *types.Filter) (types.Subscription, error) {
	return t.defaultTagger.Subscribe(subscriptionID, filter)
}
