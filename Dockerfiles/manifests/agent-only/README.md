The kubernetes manifests found in this directory have been automatically generated
from the [helm chart `datadog/datadog`](https://github.com/DataDog/helm-charts/tree/master/charts/datadog)
version 3.79.0 with the following `values.yaml`:

```yaml
datadog:
  collectEvents: true
  leaderElection: true
  apm:
    socketEnabled: false
  processAgent:
    enabled: false
    containerCollection: false
    processDiscovery: false
```
