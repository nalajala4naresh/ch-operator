package volumeutil

import (
	"context"
	"errors"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	cdiv1beta1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1beta1 "github.com/nalajala4naresh/chvmm-api/v1beta1"
)

const VM_LABEL = "virtmanager.quillpen.io/vm"
const SNAP_LABEL = "virtmanager.quillpen.io/snap"

func IsBlock(ctx context.Context, c client.Client, namespace string, volume v1beta1.Volume) (bool, error) {
	pvc, err := getPVC(ctx, c, namespace, volume)
	if err != nil {
		ctrl.Log.Error(err, "Get PVC Failure (ISBlock) func", "volume", volume.Name)
		return false, err
	}
	if pvc == nil {
		return false, errors.New("pvc not found")
	}
	return pvc.Spec.VolumeMode != nil && *pvc.Spec.VolumeMode == corev1.PersistentVolumeBlock, nil
}

func IsReady(ctx context.Context, c client.Client, namespace string, volume v1beta1.Volume) (bool, error) {
	if volume.DataVolume == nil && volume.VirtualDisk == nil {
		return true, nil
	}
	pvc, err := getPVC(ctx, c, namespace, volume)
	if err != nil {
		ctrl.Log.Error(err, "Get PVC Failure (ISReady)", "volume", volume.Name)
		return false, err
	}

	if pvc == nil {
		ctrl.Log.Error(errors.New("pvc is nil"), "Pvc is nil", "volume", volume.Name)
		return false, nil
	}

	var getDataVolumeFunc = func(name, namespace string) (*cdiv1beta1.DataVolume, error) {
		var dv cdiv1beta1.DataVolume
		dvKey := types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}
		if err := c.Get(ctx, dvKey, &dv); err != nil {
			ctrl.Log.Error(err, "Unable to Get DataVolume", "volume", volume.Name)

			return nil, err
		}
		return &dv, nil
	}
	return cdiv1beta1.IsPopulated(pvc, getDataVolumeFunc)
}

func getPVC(ctx context.Context, c client.Client, namespace string, volume v1beta1.Volume) (*corev1.PersistentVolumeClaim, error) {
	var pvcName string
	if volume.PersistentVolumeClaim != nil {
		pvcName = volume.PersistentVolumeClaim.ClaimName
	} else if volume.DataVolume != nil {
		pvcName = volume.DataVolume.VolumeName
	} else if volume.VirtualDisk != nil {
		pvcName = volume.VirtualDisk.VirtualDiskName
	}
	if pvcName == "" {
		return nil, errors.New("volume is not on a PVC")
	}

	pvcKey := types.NamespacedName{
		Namespace: namespace,
		Name:      pvcName,
	}
	if namespace == "" {
		pvcKey.Namespace = "default"
	}
	var pvc corev1.PersistentVolumeClaim
	if err := c.Get(ctx, pvcKey, &pvc); err != nil {
		if apierrors.IsNotFound(err) {
			ctrl.Log.Error(err, "pvc not found", "volume", volume.Name, "pvc", pvcKey, "namespace", namespace)

			return nil, nil
		}

		return nil, err
	}
	return &pvc, nil
}

func RecoverSnapShotToPVC(ctx context.Context, c client.Client, snapname, namespace, pvcname, stroageClass string) error {

	apigroup := "snapshot.storage.k8s.io"
	kind := "VolumeSnapshot"
	var epvc corev1.PersistentVolumeClaim
	pvcnn := types.NamespacedName{Namespace: namespace, Name: pvcname}
	err := c.Get(ctx, pvcnn, &epvc)
	// if pvc already exsits by the name, don't duplicate the work
	if err == nil {
		return nil
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcname,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			DataSource:  &corev1.TypedLocalObjectReference{APIGroup: &apigroup, Kind: kind, Name: snapname},
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("20Gi"), //change this later
				},
			},
		},
	}

	err = c.Create(ctx, pvc)

	return err

}
