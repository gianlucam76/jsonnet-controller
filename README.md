# jsonnet-controller
A Jsonnetcontroller. It can fetch Jsonnet files from:

1. Flux Sources (GitRepository/OCIRepository/Bucket)
2. ConfigMap/Secret

process those files programmatically invoking jsonnet go module and store the output in its Status section. Sveltos addon-manager can then be used to deploy the output of the ytt-controller in all selected managed clusters.

## Install

kubectl apply -f https://raw.githubusercontent.com/gianlucam76/jsonnet-controller/main/manifest/manifest.yaml
or if you want a specific version

kubectl apply -f https://raw.githubusercontent.com/gianlucam76/jsonnet-controller/<tag>/manifest/manifest.yaml

## Contributing

❤️ Your contributions are always welcome! If you want to contribute, have questions, noticed any bug or want to get the latest project news, you can connect with us in the following ways:

Read contributing guidelines
Open a bug/feature enhancement on github contributions welcome
Chat with us on the Slack in the #projectsveltos channel Slack
Contact Us
