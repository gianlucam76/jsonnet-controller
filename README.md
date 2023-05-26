# jsonnet-controller
A Jsonnetcontroller. It can fetch Jsonnet files from:

1. Flux Sources (GitRepository/OCIRepository/Bucket)
2. ConfigMap/Secret

process those files programmatically invoking jsonnet go module and store the output in its Status section. Sveltos addon-manager can then be used to deploy the output of the jsonnet-controller in all selected managed clusters.

<img src="https://github.com/projectsveltos/sveltos/blob/e045d8cb059ac7796a00470a61c5759f1389746f/docs/assets/flux-jsonnet-sveltos.png">


## Install

```bash
kubectl apply -f https://raw.githubusercontent.com/gianlucam76/jsonnet-controller/main/manifest/manifest.yaml
```

or if you want a specific version

```bash
kubectl apply -f https://raw.githubusercontent.com/gianlucam76/jsonnet-controller/<tag>/manifest/manifest.yaml
```


## Using Flux GitRepository

For instance, this Github repository https://github.com/gianlucam76/jsonnet-examples contains jsonnet files. 
You can use Flux to sync from it and then simply post this [JsonnetSource](https://github.com/gianlucam76/jsonnet-controller/blob/main/api/v1alpha1/jsonnetsource_types.go) CRD instance.
The jsonnet-controller will detect when Flux has synced the repo (and anytime there is a change), will programatically invoke jsonnet go module and store the outcome in its Status.Resources field.

```yaml
apiVersion: extension.projectsveltos.io/v1alpha1
kind: JsonnetSource
metadata:
  name: jsonnetsource-flux
spec:
  namespace: flux-system
  name: flux-system
  kind: GitRepository
  path: ./variables/deployment.jsonnet
  variables:
    deploymentName: eng
    namespace: staging
    replicas: "3"
```

```yaml
apiVersion: extension.projectsveltos.io/v1alpha1
kind: JsonnetSource
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"extension.projectsveltos.io/v1alpha1","kind":"JsonnetSource","metadata":{"annotations":{},"name":"jsonnetsource-flux","namespace":"default"},"spec":{"kind":"GitRepository","name":"flux-system","namespace":"flux-system","path":"./variables/deployment.jsonnet","variables":{"deploymentName":"eng","namespace":"staging","replicas":"3"}}}
  creationTimestamp: "2023-05-26T06:55:13Z"
  generation: 3
  name: jsonnetsource-flux
  namespace: default
  resourceVersion: "39826"
  uid: b4cc7584-528d-4938-b6ed-74ec5ba1d760
spec:
  kind: GitRepository
  name: flux-system
  namespace: flux-system
  path: ./variables/deployment.jsonnet
  variables:
    deploymentName: eng
    namespace: staging
    replicas: "3"
status:
  resources: |
    {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"labels":{"app":"eng"},"name":"eng","namespace":"staging"},"spec":{"replicas":3,"selector":{"matchLabels":{"app":"eng"}},"template":{"metadata":{"labels":{"app":"eng"}},"spec":{"containers":[{"image":"nginx:latest","name":"my-container","ports":[{"containerPort":80}]}]}}}}
```

Sveltos can used at this point to deploy resources in managed clusters:

```yaml
apiVersion: config.projectsveltos.io/v1alpha1
kind: ClusterProfile
metadata:
  name: deploy-resources
spec:
  clusterSelector: env=fv
  templateResourceRefs:
  - resource:
      apiVersion: extension.projectsveltos.io/v1alpha1
      kind: JsonnetSource
      name: jsonnetsource-flux
      namespace: default
    identifier: JsonnetSource
  policyRefs:
  - kind: ConfigMap
    name: info
    namespace: default
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: info
  namespace: default
  annotations:
    projectsveltos.io/template: "true"  # add annotation to indicate Sveltos content is a template
data:
  resource.yaml: |
    {{ (index .MgtmResources "JsonnetSource").status.resources }}
```

```bash
kubectl exec -it -n projectsveltos  sveltosctl-0   -- ./sveltosctl show addons 
+-------------------------------------+-----------------+-----------+------+---------+-------------------------------+------------------+
|               CLUSTER               |  RESOURCE TYPE  | NAMESPACE | NAME | VERSION |             TIME              | CLUSTER PROFILES |
+-------------------------------------+-----------------+-----------+------+---------+-------------------------------+------------------+
| default/sveltos-management-workload | apps:Deployment | staging   | eng  | N/A     | 2023-05-26 00:24:57 -0700 PDT | deploy-resources |
+-------------------------------------+-----------------+-----------+------+---------+-------------------------------+------------------+
```

## Using ConfigMap/Secret

JsonnetSource can also reference ConfigMap/Secret. For instance, we can create a ConfigMap whose BinaryData section contains jsonnet files.

```bash
tar -czf jsonnet.tar.gz -C ~mgianluc/go/src/github.com/gianlucam76/jsonnet-examples/multiple-files .
kubectl create configmap jsonnet --from-file=jsonnet.tar.gz=jsonnet.tar.gz 
```

Then we can have JsonnetSource reference this ConfigMap instance

```yaml
apiVersion: extension.projectsveltos.io/v1alpha1
kind: JsonnetSource
metadata:
  name: jsonnetsource-configmap
spec:
  namespace: default
  name: jsonnet
  kind: ConfigMap
  path: ./main.jsonnet
  variables:
    namespace: production
```

and the controller will programmatically execute jsonnet go module and store the outcome in Status.Results.

```yaml
apiVersion: extension.projectsveltos.io/v1alpha1
kind: JsonnetSource
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"extension.projectsveltos.io/v1alpha1","kind":"JsonnetSource","metadata":{"annotations":{},"name":"jsonnetsource-configmap","namespace":"default"},"spec":{"kind":"ConfigMap","name":"jsonnet","namespace":"default","path":"./main.jsonnet","variables":{"namespace":"production"}}}
  creationTimestamp: "2023-05-26T08:28:48Z"
  generation: 1
  name: jsonnetsource-configmap
  namespace: default
  resourceVersion: "121599"
  uid: eea93390-771d-4176-92fe-2b761b803764
spec:
  kind: ConfigMap
  name: jsonnet
  namespace: default
  path: ./main.jsonnet
  variables:
    namespace: production
status:
  resources: |
    ---
    {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"my-deployment","namespace":"production"},"spec":{"replicas":3,"selector":{"matchLabels":{"app":"my-app"}},"template":{"metadata":{"labels":{"app":"my-app"}},"spec":{"containers":[{"image":"my-image:latest","name":"my-container","ports":[{"containerPort":8080}]}]}}}}
    ---
    {"apiVersion":"v1","kind":"Service","metadata":{"name":"my-service","namespace":"production"},"spec":{"ports":[{"port":80,"protocol":"TCP","targetPort":8080}],"selector":{"app":"my-app"},"type":"LoadBalancer"}}
```

At this point [Sveltos addon-manager](https://github.com/projectsveltos/addon-manager) to use the output of the jsonnet-controller and deploy those resources in all selected managed clusters. To know more refer to [Sveltos documentation](https://projectsveltos.github.io/sveltos/ytt_extension/)

## Contributing 

❤️ Your contributions are always welcome! If you want to contribute, have questions, noticed any bug or want to get the latest project news, you can connect with us in the following ways:

1. Read contributing [guidelines](CONTRIBUTING.md)
2. Open a bug/feature enhancement on github [![contributions welcome](https://img.shields.io/badge/contributions-welcome-brightgreen.svg?style=flat)](https://github.com/projectsveltos/addon-manager/issues)
3. Chat with us on the Slack in the #projectsveltos channel [![Slack](https://img.shields.io/badge/join%20slack-%23projectsveltos-brighteen)](https://join.slack.com/t/projectsveltos/shared_invite/zt-1hraownbr-W8NTs6LTimxLPB8Erj8Q6Q)
4. [Contact Us](mailto:support@projectsveltos.io)
