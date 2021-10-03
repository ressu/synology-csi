# Deploying synology-csi

This directory contains an example configuration for the CSI driver. The
configuration uses
[external-attacher](https://github.com/kubernetes-csi/external-attacher),
[external-provisioner](https://github.com/kubernetes-csi/external-provisioner)
and [external-resizer](https://github.com/kubernetes-csi/external-resizer).
Example includes relevant RBAC rules for the components deployed.

This example configuration is compatible with Kubernetes v1.20+

# Customising the deployment

To customize the deployment, create a directory for your configuration. This
example directory has [mydeployment](mydeployment/) which illustrates how
layering of Kustomize configurations works. But individual parts are broken
down below.

A minimal configuration for layering would be:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

# Define the source for what to apply
# Using microk8s deployment here as a base
resources:
  - ../microk8s
```

The `secretGenerator` can be used to manage secrets in deployments. Create a
[syno-config.yml](mydeployment/syno-config.yml) in your deployment directory
and use `secretGenerator` to include the file in the deployment.

```yaml
secretGenerator:
  - name: synology-config
    files:
      - "syno-config.yml"
```

Other values from the main resource files can be overridden by defining new values in
the [kustomization.yaml](mydeployment/kustomization.yaml). Possible values are
described in [Kustomize
documentation](https://kubectl.docs.kubernetes.io/references/kustomize/kustomization/).

```yaml
namespace: syno-nas

images:
  - name: "ghcr.io/ressu/synology-csi:v1"
    newName: "repo.local/synology-csi"
    newTag: "v1.19.0-dev"
```
