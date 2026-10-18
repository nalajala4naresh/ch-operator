# VM access: console, port-forward, SSH and actions

`ch-vmm-gateway` is an aggregated API server, similar to KubeVirt's `virt-api`. It serves VM subresources under
`subresources.cloudhypervisor.quill.today/v1beta1`. Users reach it through `kube-apiserver` with their
normal kubeconfig, so access is controlled by RBAC and recorded in the audit log.

```
kubectl ch console myvm
  -> kube-apiserver (authenticates the user, proxies to the APIService)
  -> ch-vmm-gateway      (SubjectAccessReview on virtualmachines/console, finds the VM's node)
  -> ch-vmm-daemon   (on that node; mTLS, accepts only ch-vmm-gateway's client certificate)
  -> cloud-hypervisor serial socket / VM pod IP
```

## Client

Install the `kubectl-ch` binary from the release on your `PATH`:

```sh
kubectl ch console myvm                     # serial console, Ctrl+] to exit
kubectl ch port-forward myvm 8080:80        # local 8080 -> VM port 80
kubectl ch ssh ubuntu@myvm                  # ssh through port-forward (uses local ssh)
kubectl ch pause|unpause|restart myvm
kubectl ch migrate myvm                     # creates a VirtualMachineMigration
```

## API

| Subresource | Request | RBAC verb |
|---|---|---|
| `virtualmachines/console` | WebSocket `GET` | `create` |
| `virtualmachines/portforward/{port}` | WebSocket `GET` | `create` |
| `virtualmachines/pause`, `unpause`, `restart` | `POST` | `create` |
| `virtualmachines/migrate` | `POST`, returns the `VirtualMachineMigration` | `create` |

Streams use the WebSocket subprotocol `plain.cloudhypervisor.quill.today`: each binary message carries raw
bytes of the console or TCP stream.

Streams are authorized as `create` even though the WebSocket handshake is a `GET`. Core Kubernetes does the
same for `pods/exec`, `attach` and `portforward` (`AuthorizePodWebsocketUpgradeCreatePermission`), because
read-only roles commonly grant `get`.

## RBAC

Two ClusterRoles are aggregated into the built-in `admin` and `edit` roles:

- `ch-vmm-vm-access`: console and port-forward.
- `ch-vmm-vm-operator`: pause, unpause, restart, migrate.

To grant one in a single namespace:

```sh
kubectl create rolebinding alice-vm-access --clusterrole=ch-vmm-vm-access --user=alice -n team-a
```

## Notes

- The serial console uses cloud-hypervisor's `Socket` mode. VMs started before this change keep a TTY
  console until they restart; their console returns 409. Serial output is no longer in `kubectl logs`.
- Port-forward connects to the VM pod IP from the VM's node, so it reaches the guest for bridge and
  masquerade interfaces on the pod network. NetworkPolicies must allow traffic from the node.
- `ch-vmm-gateway` and `ch-vmm-daemon` authenticate each other with certificates from the `ch-vmm-ca` issuer in
  `ch-vmm-system` (cert-manager).
- The gRPC `VmService` and the `vmm` CLI are deprecated. They bypass RBAC and will be removed.
