// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package catalog contains the implementation of the filtering catalogs.
package catalog

import (
	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	workloadfilter "github.com/DataDog/datadog-agent/comp/core/workloadfilter/def"
	"github.com/DataDog/datadog-agent/comp/core/workloadfilter/program"
)

// LegacyServiceMetricsProgram creates a program for filtering service metrics
func LegacyServiceMetricsProgram(config config.Component, logger log.Component) program.CELProgram {
	programName := "LegacyServiceMetricsProgram"
	var initErrors []error

	includeProgram, includeErr := createProgramFromOldFilters(config.GetStringSlice("container_include_metrics"), workloadfilter.ServiceType)
	if includeErr != nil {
		initErrors = append(initErrors, includeErr)
		logger.Warnf("Error creating include program for %s: %v", programName, includeErr)
	}

	excludeProgram, excludeErr := createProgramFromOldFilters(config.GetStringSlice("container_exclude_metrics"), workloadfilter.ServiceType)
	if excludeErr != nil {
		initErrors = append(initErrors, excludeErr)
		logger.Warnf("Error creating exclude program for %s: %v", programName, excludeErr)
	}

	return program.CELProgram{
		Name:                 programName,
		Include:              includeProgram,
		Exclude:              excludeProgram,
		InitializationErrors: initErrors,
	}
}

// LegacyServiceGlobalProgram creates a program for filtering services globally
func LegacyServiceGlobalProgram(config config.Component, logger log.Component) program.CELProgram {
	programName := "LegacyServiceGlobalProgram"
	var initErrors []error

	includeList := config.GetStringSlice("container_include")
	excludeList := config.GetStringSlice("container_exclude")
	if len(includeList) == 0 {
		// fallback and support legacy "ac_include" config
		includeList = config.GetStringSlice("ac_include")
	}
	if len(excludeList) == 0 {
		// fallback and support legacy "ac_exclude" config
		excludeList = config.GetStringSlice("ac_exclude")
	}

	includeProgram, includeErr := createProgramFromOldFilters(includeList, workloadfilter.ServiceType)
	if includeErr != nil {
		initErrors = append(initErrors, includeErr)
		logger.Warnf("Error creating include program for %s: %v", programName, includeErr)
	}

	excludeProgram, excludeErr := createProgramFromOldFilters(excludeList, workloadfilter.ServiceType)
	if excludeErr != nil {
		initErrors = append(initErrors, excludeErr)
		logger.Warnf("Error creating exclude program for %s: %v", programName, excludeErr)
	}

	return program.CELProgram{
		Name:                 programName,
		Include:              includeProgram,
		Exclude:              excludeProgram,
		InitializationErrors: initErrors,
	}
}

// ServiceADAnnotationsProgram creates a program for filtering services based on AD annotations
func ServiceADAnnotationsProgram(_ config.Component, logger log.Component) program.CELProgram {
	programName := "ServiceADAnnotationsProgram"

	var initErrors []error
	// Use 'in' operator to safely check if annotation exists before accessing it
	excludeFilter := `(("ad.datadoghq.com/exclude") in service.annotations && 
		 service.annotations["ad.datadoghq.com/exclude"] in ["1", "t", "T", "true", "TRUE", "True"])`

	excludeProgram, err := createCELProgram(excludeFilter, workloadfilter.ServiceType)
	if err != nil {
		initErrors = append(initErrors, err)
		logger.Warnf("Error creating CEL filtering program for %s: %v", programName, err)
	}

	return program.CELProgram{
		Name:                 programName,
		Exclude:              excludeProgram,
		InitializationErrors: initErrors,
	}
}

// ServiceADAnnotationsMetricsProgram creates a program for filtering services metrics based on AD annotations
func ServiceADAnnotationsMetricsProgram(_ config.Component, logger log.Component) program.CELProgram {
	programName := "ServiceADAnnotationsMetricsProgram"

	var initErrors []error
	// Use 'in' operator to safely check if annotation exists before accessing it
	excludeFilter := `(("ad.datadoghq.com/metrics_exclude") in service.annotations && 
		 service.annotations["ad.datadoghq.com/metrics_exclude"] in ["1", "t", "T", "true", "TRUE", "True"])`

	excludeProgram, err := createCELProgram(excludeFilter, workloadfilter.ServiceType)
	if err != nil {
		initErrors = append(initErrors, err)
		logger.Warnf("Error creating CEL filtering program for %s: %v", programName, err)
	}

	return program.CELProgram{
		Name:                 programName,
		Exclude:              excludeProgram,
		InitializationErrors: initErrors,
	}
}
