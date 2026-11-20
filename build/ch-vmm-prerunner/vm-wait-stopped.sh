#!/bin/sh
# preStop hook for the VM pod. Deleting the pod is how a VM is stopped; the node daemon
# (ch-vmm-daemon) presses the guest's ACPI power button and forcibly stops the VM when the
# grace period runs out. This hook only waits, up to $1 seconds, for the VM to stop, so the
# kubelet's SIGTERM (which s6 would pass straight to Cloud Hypervisor) doesn't cut the
# guest shutdown short.
#
# Exits 0 in every case so it never blocks pod termination beyond its timeout.

sock=/var/run/ch-vmm/vm.sock
timeout="${1:-45}"

vm_state() {
  # Works for both compact and pretty-printed JSON.
  ch-remote --api-socket "$sock" info 2>/dev/null |
    grep -o '"state": *"[A-Za-z]*"' | head -n 1 | sed 's/.*"\([A-Za-z]*\)"$/\1/'
}

i=0
while [ "$i" -lt "$timeout" ]; do
  state="$(vm_state)"
  if [ "$state" != "Running" ] && [ "$state" != "Paused" ]; then
    echo "vm-wait-stopped: VM stopped (state: ${state:-gone}) after ${i}s"
    exit 0
  fi
  sleep 1
  i=$((i + 1))
done

echo "vm-wait-stopped: VM still running after ${timeout}s, leaving it to SIGTERM"
exit 0
