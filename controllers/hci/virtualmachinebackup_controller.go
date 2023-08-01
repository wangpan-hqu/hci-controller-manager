/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/v2/pkg/apis/volumesnapshot/v1beta1"
	"github.com/longhorn/backupstore"
	lhv1beta1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	longhorntypes "github.com/longhorn/longhorn-manager/types"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/pointer"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/wangpan-hqu/hci-controller-manager/pkg/settings"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1beta1 "github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
)

const (
	backupControllerName         = "hci-vm-backup-controller"
	snapshotControllerName       = "volume-snapshot-controller"
	longhornBackupControllerName = "longhorn-backup-controller"
	pvcControllerName            = "hci-pvc-controller"
	vmBackupKindName             = "VirtualMachineBackup"

	volumeSnapshotCreateEvent = "VolumeSnapshotCreated"

	backupTargetAnnotation       = "backup.hci.wjyl.com/backup-target"
	backupBucketNameAnnotation   = "backup.hci.wjyl.com/bucket-name"
	backupBucketRegionAnnotation = "backup.hci.wjyl.com/bucket-region"
)

var vmBackupKind = v1beta1.GroupVersion.WithKind(vmBackupKindName)

// VirtualMachineBackupReconciler reconciles a VirtualMachineBackup object
type VirtualMachineBackupReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	Recorder record.EventRecorder
}

//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinebackups,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinebackups/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=virtualmachinebackups/finalizers,verbs=update
//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumes,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch;create;update;delete

func (h *VirtualMachineBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	logrus.Info("get backup: " + req.String())
	vmBackup := &v1beta1.VirtualMachineBackup{}
	if err := h.Get(ctx, req.NamespacedName, vmBackup); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if vmBackup.Status == nil {
		// 初始化状态字段
		logrus.Info("init backup status: " + req.String())
		t := metav1.Now()
		vmBackup.Status = &v1beta1.VirtualMachineBackupStatus{
			CreationTime: &t,
		}
	}

	if _, err := h.OnBackupChange(vmBackup); err != nil {
		return ctrl.Result{}, err
	}

	if vmBackup.GetDeletionTimestamp() == nil {
		if !util.HasFinalizer(vmBackup, backupControllerName) {
			// 添加finalizer
			logrus.Info("add finalizer: " + req.String())
			vmBackup = &v1beta1.VirtualMachineBackup{}
			if err := h.Get(ctx, req.NamespacedName, vmBackup); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
			util.AddFinalizer(vmBackup, backupControllerName)
			err := h.Update(context.TODO(), vmBackup)
			return ctrl.Result{}, err
		}
		logrus.Info("skip finalizer: " + req.String())
	} else {
		if !util.HasFinalizer(vmBackup, backupControllerName) {
			logrus.Info("finalizer not found: " + req.String())
			return ctrl.Result{}, nil
		}

		vmBackup = &v1beta1.VirtualMachineBackup{}
		if err := h.Get(ctx, req.NamespacedName, vmBackup); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		logrus.Info("delete finalizer: " + req.String())
		util.RemoveFinalizer(vmBackup, backupControllerName)
		if err := h.Update(context.TODO(), vmBackup); err != nil {
			return ctrl.Result{}, err
		}

		vmBackup = &v1beta1.VirtualMachineBackup{}
		if err := h.Get(ctx, req.NamespacedName, vmBackup); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		logrus.Info("remove backup: " + req.String())
		if _, err := h.OnBackupRemove(req.String(), vmBackup); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// OnBackupChange handles vm backup object on change and reconcile vm backup status
func (h *VirtualMachineBackupReconciler) OnBackupChange(vmBackup *v1beta1.VirtualMachineBackup) (ctrl.Result, error) {
	if vmBackup == nil || vmBackup.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}

	if IsBackupReady(vmBackup) {
		return ctrl.Result{}, h.handleBackupReady(vmBackup)
	}

	logrus.Debugf("OnBackupChange: vmBackup name:%s", vmBackup.Name)
	// set vmBackup init status
	if isBackupMissingStatus(vmBackup) {
		// We cannot get VM outside this block, because we also can sync VMBackup from remote target.
		// A VMBackup without status is a new VMBackup, so it must have sourceVM.
		sourceVM, err := h.getBackupSource(vmBackup)
		if err != nil {
			return ctrl.Result{}, h.setStatusError(vmBackup, err)
		}
		// check if the VM is running, if not make sure the volumes are mounted to the host
		if !sourceVM.Status.Ready || !sourceVM.Status.Created {
			if err := h.mountLonghornVolumes(sourceVM); err != nil {
				return ctrl.Result{}, h.setStatusError(vmBackup, err)
			}
		}

		return ctrl.Result{}, h.initBackup(vmBackup, sourceVM)
	}

	// TODO, make sure status is initialized, and "Lock" the source VM by adding a finalizer and setting snapshotInProgress in status

	_, csiDriverVolumeSnapshotClassMap, err := h.getCSIDriverMap(vmBackup)
	if err != nil {
		return ctrl.Result{}, h.setStatusError(vmBackup, err)
	}

	// create volume snapshots if not exist
	if result, err := h.reconcileVolumeSnapshots(vmBackup, csiDriverVolumeSnapshotClassMap); result != nil || err != nil {
		return *result, h.setStatusError(vmBackup, err)
	}

	// reconcile backup status of volume backups, validate if those volumeSnapshots are ready to use
	if err := h.updateConditions(vmBackup); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// OnBackupRemove remove remote vm backup metadata
func (h *VirtualMachineBackupReconciler) OnBackupRemove(key string, vmBackup *v1beta1.VirtualMachineBackup) (*v1beta1.VirtualMachineBackup, error) {
	if vmBackup == nil || vmBackup.Status == nil || vmBackup.Status.BackupTarget == nil {
		return nil, nil
	}

	target, err := settings.DecodeBackupTarget(h.GetBackupTargetSetting())
	if err != nil {
		return nil, err
	}

	if err := h.deleteVMBackupMetadata(vmBackup, target); err != nil {
		return nil, err
	}

	// Since VolumeSnapshot and VolumeSnapshotContent has finalizers,
	// when we delete VM Backup and its backup target is not same as current backup target,
	// VolumeSnapshot and VolumeSnapshotContent may not be deleted immediately.
	// We should force delete them to avoid that users re-config backup target back and associated LH Backup may be deleted.
	if !IsBackupTargetSame(vmBackup.Status.BackupTarget, target) {
		if err := h.forceDeleteVolumeSnapshotAndContent(vmBackup.Namespace, vmBackup.Status.VolumeBackups); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (h *VirtualMachineBackupReconciler) deleteVMBackupMetadata(vmBackup *v1beta1.VirtualMachineBackup, target *settings.BackupTarget) error {
	var err error
	if target == nil {
		if target, err = settings.DecodeBackupTarget(h.GetBackupTargetSetting()); err != nil {
			return err
		}
	}

	// when backup target has been reset to default, skip following
	if target.IsDefaultBackupTarget() {
		logrus.Debugf("vmBackup delete:%s, backup target is default, skip", vmBackup.Name)
		return nil
	}

	if !IsBackupTargetSame(vmBackup.Status.BackupTarget, target) {
		return nil
	}

	if target.Type == settings.S3BackupType {
		secret := &corev1.Secret{}
		err = h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: util.BackupTargetSecretName}, secret)
		if err != nil {
			return err
		}
		os.Setenv(AWSAccessKey, string(secret.Data[AWSAccessKey]))
		os.Setenv(AWSSecretKey, string(secret.Data[AWSSecretKey]))
		os.Setenv(AWSEndpoints, string(secret.Data[AWSEndpoints]))
		os.Setenv(AWSCERT, string(secret.Data[AWSCERT]))
	}

	bsDriver, err := backupstore.GetBackupStoreDriver(ConstructEndpoint(target))
	if err != nil {
		return err
	}

	destURL := filepath.Join(metadataFolderPath, getVMBackupMetadataFileName(vmBackup.Namespace, vmBackup.Name))
	if exist := bsDriver.FileExists(destURL); exist {
		logrus.Debugf("delete vm backup metadata %s/%s in backup target %s", vmBackup.Namespace, vmBackup.Name, target.Type)
		return bsDriver.Remove(destURL)
	}

	return nil
}

func (h *VirtualMachineBackupReconciler) forceDeleteVolumeSnapshotAndContent(namespace string, volumeBackups []v1beta1.VolumeBackup) error {
	for _, volumeBackup := range volumeBackups {
		volumeSnapshot, err := h.getVolumeSnapshot(namespace, *volumeBackup.Name)
		if err != nil {
			return err
		}
		if volumeSnapshot == nil {
			continue
		}

		// remove finalizers in VolumeSnapshot and force delete it
		volumeSnapshotCpy := volumeSnapshot.DeepCopy()
		volumeSnapshot.Finalizers = []string{}
		logrus.Debugf("remove finalizers in volume snapshot %s/%s", namespace, *volumeBackup.Name)
		if err = h.Update(context.TODO(), volumeSnapshotCpy); err != nil {
			return err
		}
		logrus.Debugf("delete volume snapshot %s/%s", namespace, *volumeBackup.Name)
		if err = h.Delete(context.TODO(), volumeSnapshotCpy); err != nil {
			return err
		}

		if volumeSnapshot.Status == nil || volumeSnapshot.Status.BoundVolumeSnapshotContentName == nil {
			continue
		}
		volumeSnapshotContent := &snapshotv1.VolumeSnapshotContent{}
		err = h.Get(context.TODO(), types.NamespacedName{Name: *volumeSnapshot.Status.BoundVolumeSnapshotContentName}, volumeSnapshotContent)
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			continue
		}

		// remove finalizers in VolumeSnapshotContent and force delete it
		volumeSnapshotContentCpy := volumeSnapshotContent.DeepCopy()
		volumeSnapshotContentCpy.Finalizers = []string{}
		logrus.Debugf("remove finalizers in volume snapshot content %s", *volumeSnapshot.Status.BoundVolumeSnapshotContentName)
		if err = h.Update(context.TODO(), volumeSnapshotContentCpy); err != nil {
			return err
		}
		logrus.Debugf("delete volume snapshot content %s", *volumeSnapshot.Status.BoundVolumeSnapshotContentName)
		if err := h.Delete(context.TODO(), volumeSnapshotContentCpy); err != nil {
			return err
		}
	}
	return nil
}

// reconcileVolumeSnapshots create volume snapshot if not exist.
// For vm backup from a existent VM, we create volume snapshot from pvc.
// For vm backup from syncing vm backup metadata, we create volume snapshot from volume snapshot content.
func (h *VirtualMachineBackupReconciler) reconcileVolumeSnapshots(vmBackup *v1beta1.VirtualMachineBackup, csiDriverVolumeSnapshotClassMap map[string]snapshotv1.VolumeSnapshotClass) (*ctrl.Result, error) {
	vmBackupCpy := vmBackup.DeepCopy()
	for i, volumeBackup := range vmBackupCpy.Status.VolumeBackups {
		if volumeBackup.Name == nil {
			logrus.Info("skip empty VolumeBackup")
			continue
		}

		snapshotName := *volumeBackup.Name
		volumeSnapshot, err := h.getVolumeSnapshot(vmBackupCpy.Namespace, snapshotName)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}

		if volumeSnapshot != nil && volumeSnapshot.DeletionTimestamp != nil {
			logrus.Infof("volumeSnapshot %s/%s is being deleted, requeue vm backup %s/%s again",
				volumeSnapshot.Namespace, volumeSnapshot.Name,
				vmBackup.Namespace, vmBackup.Name)
			return &ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}

		logrus.Infof("get volumeSnapshot %s/%s: ", vmBackupCpy.Namespace, snapshotName)

		if volumeSnapshot == nil {
			volumeSnapshotClass := csiDriverVolumeSnapshotClassMap[volumeBackup.CSIDriverName]
			volumeSnapshot, err = h.createVolumeSnapshot(vmBackupCpy, volumeBackup, &volumeSnapshotClass)
			if err != nil {
				logrus.Infof("create volumeSnapshot %s/%s error: %v", vmBackupCpy.Namespace, snapshotName, err)
				return nil, err
			}
			logrus.Infof("create volumeSnapshot %s/%s", vmBackupCpy.Namespace, snapshotName)
		}

		if volumeSnapshot.Status != nil {
			vmBackupCpy.Status.VolumeBackups[i].ReadyToUse = volumeSnapshot.Status.ReadyToUse
			vmBackupCpy.Status.VolumeBackups[i].CreationTime = volumeSnapshot.Status.CreationTime
			vmBackupCpy.Status.VolumeBackups[i].Error = translateError(volumeSnapshot.Status.Error)
		}
	}

	if !reflect.DeepEqual(vmBackup.Status, vmBackupCpy.Status) {
		if err := h.Update(context.TODO(), vmBackupCpy); err != nil {
			return nil, err
		}
	}

	return nil, nil
}

func (h *VirtualMachineBackupReconciler) createVolumeSnapshot(vmBackup *v1beta1.VirtualMachineBackup, volumeBackup v1beta1.VolumeBackup, volumeSnapshotClass *snapshotv1.VolumeSnapshotClass) (*snapshotv1.VolumeSnapshot, error) {
	logrus.Info("attempting to create VolumeSnapshot " + *volumeBackup.Name)
	var (
		sourceStorageClassName string
		sourceImageID          string
		sourceProvisioner      string
	)

	volumeSnapshotSource := snapshotv1.VolumeSnapshotSource{}
	// If LonghornBackupName exists, it means the VM Backup has associated LH Backup.
	// In this case, we should create volume snapshot from LH Backup instead of from current PVC.
	if volumeBackup.LonghornBackupName != nil {
		volumeSnapshotContent, err := h.createVolumeSnapshotContent(vmBackup, volumeBackup, volumeSnapshotClass)
		if err != nil {
			return nil, err
		}
		volumeSnapshotSource.VolumeSnapshotContentName = &volumeSnapshotContent.Name
	} else {
		pvc := &corev1.PersistentVolumeClaim{}
		err := h.Get(context.TODO(),
			types.NamespacedName{Namespace: volumeBackup.PersistentVolumeClaim.ObjectMeta.Namespace, Name: volumeBackup.PersistentVolumeClaim.ObjectMeta.Name}, pvc)
		if err != nil {
			return nil, err
		}
		sourceStorageClassName = *pvc.Spec.StorageClassName
		sourceImageID = pvc.Annotations[util.AnnotationImageID]
		sourceProvisioner = util.GetProvisionedPVCProvisioner(pvc)
		volumeSnapshotSource.PersistentVolumeClaimName = &volumeBackup.PersistentVolumeClaim.ObjectMeta.Name
	}

	snapshot := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      *volumeBackup.Name,
			Namespace: vmBackup.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: v1beta1.GroupVersion.String(),
					Kind:       vmBackupKind.Kind,
					Name:       vmBackup.Name,
					UID:        vmBackup.UID,
					Controller: pointer.BoolPtr(true),
				},
			},
			Annotations: map[string]string{},
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			Source:                  volumeSnapshotSource,
			VolumeSnapshotClassName: pointer.StringPtr(volumeSnapshotClass.Name),
		},
	}

	if sourceStorageClassName != "" {
		snapshot.Annotations[util.AnnotationStorageClassName] = sourceStorageClassName
	}
	if sourceImageID != "" {
		snapshot.Annotations[util.AnnotationImageID] = sourceImageID
	}
	if sourceProvisioner != "" {
		snapshot.Annotations[util.AnnotationStorageProvisioner] = sourceProvisioner
	}

	logrus.Infof("create VolumeSnapshot %s/%s", vmBackup.Namespace, *volumeBackup.Name)
	err := h.Create(context.TODO(), snapshot)
	if err != nil {
		return nil, err
	}

	h.Recorder.Eventf(
		vmBackup,
		corev1.EventTypeNormal,
		volumeSnapshotCreateEvent,
		"Successfully created VolumeSnapshot %s",
		snapshot.Name,
	)

	volumeSnapshot := &snapshotv1.VolumeSnapshot{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: snapshot.Namespace, Name: snapshot.Name}, volumeSnapshot)
	return volumeSnapshot, err
}

func (h *VirtualMachineBackupReconciler) createVolumeSnapshotContent(
	vmBackup *v1beta1.VirtualMachineBackup,
	volumeBackup v1beta1.VolumeBackup,
	snapshotClass *snapshotv1.VolumeSnapshotClass,
) (*snapshotv1.VolumeSnapshotContent, error) {
	logrus.Debugf("attempting to create VolumeSnapshotContent %s", getVolumeSnapshotContentName(volumeBackup))
	snapshotContent := &snapshotv1.VolumeSnapshotContent{}
	err := h.Get(context.TODO(), types.NamespacedName{Name: getVolumeSnapshotContentName(volumeBackup)}, snapshotContent)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	} else if err == nil {
		return snapshotContent, nil
	}

	lhBackup := &lhv1beta1.Backup{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: *volumeBackup.LonghornBackupName}, lhBackup)
	if err != nil {
		return nil, err
	}
	snapshotHandle := fmt.Sprintf("bs://%s/%s", volumeBackup.PersistentVolumeClaim.ObjectMeta.Name, lhBackup.Name)

	logrus.Debugf("create VolumeSnapshotContent %s", getVolumeSnapshotContentName(volumeBackup))
	err = h.Create(context.TODO(), &snapshotv1.VolumeSnapshotContent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getVolumeSnapshotContentName(volumeBackup),
			Namespace: vmBackup.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: v1beta1.GroupVersion.String(),
					Kind:       vmBackupKind.Kind,
					Name:       vmBackup.Name,
					UID:        vmBackup.UID,
					Controller: pointer.BoolPtr(true),
				},
			},
		},
		Spec: snapshotv1.VolumeSnapshotContentSpec{
			Driver:         "driver.longhorn.io",
			DeletionPolicy: snapshotv1.VolumeSnapshotContentDelete,
			Source: snapshotv1.VolumeSnapshotContentSource{
				SnapshotHandle: pointer.StringPtr(snapshotHandle),
			},
			VolumeSnapshotClassName: pointer.StringPtr(snapshotClass.Name),
			VolumeSnapshotRef: corev1.ObjectReference{
				Name:      *volumeBackup.Name,
				Namespace: vmBackup.Namespace,
			},
		},
	})

	return snapshotContent, err
}

func (h *VirtualMachineBackupReconciler) getVolumeSnapshot(namespace, name string) (*snapshotv1.VolumeSnapshot, error) {
	snapshot := &snapshotv1.VolumeSnapshot{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, snapshot)
	if err != nil {
		return nil, err
	}

	return snapshot, nil
}

func (h *VirtualMachineBackupReconciler) handleBackupReady(vmBackup *v1beta1.VirtualMachineBackup) error {
	// We add CSIDriverVolumeSnapshotClassNameMap since v1.1.0.
	// For backport to v1.0.x, we construct the map from VolumeBackups.
	var err error
	if vmBackup, err = h.configureCSIDriverVolumeSnapshotClassNames(vmBackup); err != nil {
		return err
	}

	// only backup type needs to configure backup target and upload metadata
	if vmBackup.Spec.Type == v1beta1.Snapshot {
		return nil
	}

	// We've changed backup target information to status since v1.0.0.
	// For backport to v0.3.0, we move backup target information from annotation to status.
	if vmBackup, err = h.configureBackupTargetOnStatus(vmBackup); err != nil {
		return err
	}

	// generate vm backup metadata and upload to backup target
	return h.uploadVMBackupMetadata(vmBackup, nil)
}

func (h *VirtualMachineBackupReconciler) configureCSIDriverVolumeSnapshotClassNames(vmBackup *v1beta1.VirtualMachineBackup) (*v1beta1.VirtualMachineBackup, error) {
	if len(vmBackup.Status.CSIDriverVolumeSnapshotClassNames) != 0 {
		return vmBackup, nil
	}

	logrus.Debugf("configure csiDriverVolumeSnapshotClassNames from volumeBackups for vm backup %s/%s", vmBackup.Namespace, vmBackup.Name)
	var err error
	vmBackupCpy := vmBackup.DeepCopy()
	if vmBackupCpy.Status.CSIDriverVolumeSnapshotClassNames, _, err = h.getCSIDriverMap(vmBackup); err != nil {
		return nil, err
	}
	if err = h.Update(context.TODO(), vmBackupCpy); err != nil {
		return nil, err
	}

	vmBackupUpdated := &v1beta1.VirtualMachineBackup{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: vmBackup.GetNamespace(), Name: vmBackup.GetName()}, vmBackupUpdated)
	return vmBackupUpdated, err
}

func (h *VirtualMachineBackupReconciler) uploadVMBackupMetadata(vmBackup *v1beta1.VirtualMachineBackup, target *settings.BackupTarget) error {
	var err error
	// if users don't update VMBackup CRD, we may lose backup target data.
	if vmBackup.Status.BackupTarget == nil {
		return fmt.Errorf("no backup target in vmbackup.status")
	}

	if target == nil {
		if target, err = settings.DecodeBackupTarget(h.GetBackupTargetSetting()); err != nil {
			return err
		}
	}

	// when current backup target is default, skip following steps
	// if backup target is default, IsBackupTargetSame is true when vmBackup.Status.BackupTarget is also default value
	if target.IsDefaultBackupTarget() {
		return nil
	}

	if !IsBackupTargetSame(vmBackup.Status.BackupTarget, target) {
		return nil
	}

	if target.Type == settings.S3BackupType {
		secret := &corev1.Secret{}
		if err := h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: util.BackupTargetSecretName}, secret); err != nil {
			return err
		}
		os.Setenv(AWSAccessKey, string(secret.Data[AWSAccessKey]))
		os.Setenv(AWSSecretKey, string(secret.Data[AWSSecretKey]))
		os.Setenv(AWSEndpoints, string(secret.Data[AWSEndpoints]))
		os.Setenv(AWSCERT, string(secret.Data[AWSCERT]))
	}

	bsDriver, err := backupstore.GetBackupStoreDriver(ConstructEndpoint(target))
	if err != nil {
		return err
	}

	vmBackupMetadata := &VirtualMachineBackupMetadata{
		Name:          vmBackup.Name,
		Namespace:     vmBackup.Namespace,
		BackupSpec:    vmBackup.Spec,
		VMSourceSpec:  vmBackup.Status.SourceSpec,
		VolumeBackups: sanitizeVolumeBackups(vmBackup.Status.VolumeBackups),
		SecretBackups: vmBackup.Status.SecretBackups,
	}
	if vmBackup.Namespace == "" {
		vmBackupMetadata.Namespace = metav1.NamespaceDefault
	}

	j, err := json.Marshal(vmBackupMetadata)
	if err != nil {
		return err
	}

	shouldUpload := true
	destURL := filepath.Join(metadataFolderPath, getVMBackupMetadataFileName(vmBackup.Namespace, vmBackup.Name))
	if bsDriver.FileExists(destURL) {
		if remoteVMBackupMetadata, err := loadBackupMetadataInBackupTarget(destURL, bsDriver); err != nil {
			return err
		} else if reflect.DeepEqual(vmBackupMetadata, remoteVMBackupMetadata) {
			shouldUpload = false
		}
	}

	if shouldUpload {
		logrus.Debugf("upload vm backup metadata %s/%s to backup target %s", vmBackup.Namespace, vmBackup.Name, target.Type)
		if err := bsDriver.Write(destURL, bytes.NewReader(j)); err != nil {
			return err
		}
	}

	return nil
}

func sanitizeVolumeBackups(volumeBackups []v1beta1.VolumeBackup) []v1beta1.VolumeBackup {
	for i := 0; i < len(volumeBackups); i++ {
		volumeBackups[i].ReadyToUse = nil
		volumeBackups[i].CreationTime = nil
		volumeBackups[i].Error = nil
	}
	return volumeBackups
}

func (h *VirtualMachineBackupReconciler) configureBackupTargetOnStatus(vmBackup *v1beta1.VirtualMachineBackup) (*v1beta1.VirtualMachineBackup, error) {
	if !isBackupTargetOnAnnotation(vmBackup) {
		return vmBackup, nil
	}

	logrus.Debugf("configure backup target from annotation to status for vm backup %s/%s", vmBackup.Namespace, vmBackup.Name)
	vmBackupCpy := vmBackup.DeepCopy()
	vmBackupCpy.Status.BackupTarget = &v1beta1.BackupTarget{
		Endpoint:     vmBackup.Annotations[backupTargetAnnotation],
		BucketName:   vmBackup.Annotations[backupBucketNameAnnotation],
		BucketRegion: vmBackup.Annotations[backupBucketRegionAnnotation],
	}

	if err := h.Update(context.TODO(), vmBackupCpy); err != nil {
		return nil, err
	}

	vmBackupUpdated := &v1beta1.VirtualMachineBackup{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: vmBackup.GetNamespace(), Name: vmBackup.GetName()}, vmBackupUpdated)
	return vmBackupUpdated, err
}

func (h *VirtualMachineBackupReconciler) getBackupSource(vmBackup *v1beta1.VirtualMachineBackup) (*kubevirtv1.VirtualMachine, error) {
	var err error
	sourceVM := &kubevirtv1.VirtualMachine{}

	switch vmBackup.Spec.Source.Kind {
	case kubevirtv1.VirtualMachineGroupVersionKind.Kind:
		err = h.Get(context.TODO(), types.NamespacedName{Namespace: vmBackup.Namespace, Name: vmBackup.Spec.Source.Name}, sourceVM)
	default:
		err = fmt.Errorf("unsupported source: %+v", vmBackup.Spec.Source)
	}
	if err != nil {
		return nil, err
	} else if sourceVM.DeletionTimestamp != nil {
		return nil, fmt.Errorf("vm %s/%s is being deleted", vmBackup.Namespace, vmBackup.Spec.Source.Name)
	}

	return sourceVM, nil
}

func (h *VirtualMachineBackupReconciler) setStatusError(vmBackup *v1beta1.VirtualMachineBackup, err error) error {
	vmBackupCpy := vmBackup.DeepCopy()
	if vmBackupCpy.Status != nil {
		vmBackupCpy.Status = &v1beta1.VirtualMachineBackupStatus{}
	}

	vmBackupCpy.Status.Error = &v1beta1.Error{
		Time:    currentTime(),
		Message: pointer.StringPtr(err.Error()),
	}
	updateBackupCondition(vmBackupCpy, newProgressingCondition(corev1.ConditionFalse, "Error", err.Error()))
	updateBackupCondition(vmBackupCpy, newReadyCondition(corev1.ConditionFalse, "", "Not Ready"))

	if updateErr := h.Update(context.TODO(), vmBackupCpy); updateErr != nil {
		return updateErr
	}
	return err
}

// mountLonghornVolumes helps to mount the volumes to host if it is detached
func (h *VirtualMachineBackupReconciler) mountLonghornVolumes(vm *kubevirtv1.VirtualMachine) error {
	for _, vol := range vm.Spec.Template.Spec.Volumes {
		if vol.PersistentVolumeClaim == nil {
			continue
		}
		name := vol.PersistentVolumeClaim.ClaimName

		pvc := &corev1.PersistentVolumeClaim{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: vm.Namespace, Name: name}, pvc)
		if err != nil {
			return fmt.Errorf("failed to get pvc %s/%s, error: %s", name, vm.Namespace, err.Error())
		}

		sc := &storagev1.StorageClass{}
		err = h.Get(context.TODO(), types.NamespacedName{Name: *pvc.Spec.StorageClassName}, sc)
		if err != nil {
			return err
		}
		if sc.Provisioner != longhorntypes.LonghornDriverName {
			continue
		}

		volume := &lhv1beta1.Volume{}
		err = h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: pvc.Spec.VolumeName}, volume)
		if err != nil {
			return fmt.Errorf("failed to get volume %s/%s, error: %s", name, vm.Namespace, err.Error())
		}

		volCpy := volume.DeepCopy()
		if volume.Status.State == lhv1beta1.VolumeStateDetached || volume.Status.State == lhv1beta1.VolumeStateDetaching {
			volCpy.Spec.NodeID = volume.Status.OwnerID
		}

		if !reflect.DeepEqual(volCpy, volume) {
			logrus.Infof("mount detached volume %s to the node %s", volCpy.Name, volCpy.Spec.NodeID)
			if err = h.Update(context.TODO(), volCpy); err != nil {
				return err
			}
		}
	}
	return nil
}

// initBackup initialize VM backup status and annotation
func (h *VirtualMachineBackupReconciler) initBackup(backup *v1beta1.VirtualMachineBackup, vm *kubevirtv1.VirtualMachine) error {
	var err error
	backupCpy := backup.DeepCopy()
	backupCpy.Status = &v1beta1.VirtualMachineBackupStatus{
		ReadyToUse: pointer.BoolPtr(false),
		SourceUID:  &vm.UID,
		SourceSpec: &v1beta1.VirtualMachineSourceSpec{
			ObjectMeta: metav1.ObjectMeta{
				Name:        vm.ObjectMeta.Name,
				Namespace:   vm.ObjectMeta.Namespace,
				Annotations: vm.ObjectMeta.Annotations,
				Labels:      vm.ObjectMeta.Labels,
			},
			Spec: vm.Spec,
		},
	}

	if backupCpy.Status.VolumeBackups, err = h.getVolumeBackups(backup, vm); err != nil {
		return err
	}

	if backupCpy.Status.SecretBackups, err = h.getSecretBackups(vm); err != nil {
		return err
	}

	if backupCpy.Status.CSIDriverVolumeSnapshotClassNames, _, err = h.getCSIDriverMap(backupCpy); err != nil {
		return err
	}

	if backup.Spec.Type == v1beta1.Backup {
		target, err := settings.DecodeBackupTarget(h.GetBackupTargetSetting())
		if err != nil {
			return err
		}

		backupCpy.Status.BackupTarget = &v1beta1.BackupTarget{
			Endpoint:     target.Endpoint,
			BucketName:   target.BucketName,
			BucketRegion: target.BucketRegion,
		}
	}

	if err := h.Update(context.TODO(), backupCpy); err != nil {
		return err
	}
	return nil
}

// getVolumeBackups helps to build a list of VolumeBackup upon the volume list of backup VM
func (h *VirtualMachineBackupReconciler) getVolumeBackups(backup *v1beta1.VirtualMachineBackup, vm *kubevirtv1.VirtualMachine) ([]v1beta1.VolumeBackup, error) {
	sourceVolumes := vm.Spec.Template.Spec.Volumes
	var volumeBackups = make([]v1beta1.VolumeBackup, 0, len(sourceVolumes))

	for volumeName, pvcName := range volumeToPVCMappings(sourceVolumes) {
		pvc, err := h.getBackupPVC(vm.Namespace, pvcName)
		if err != nil {
			return nil, err
		}

		pv := &corev1.PersistentVolume{}
		err = h.Get(context.TODO(), types.NamespacedName{Name: pvc.Spec.VolumeName}, pv)
		if err != nil {
			return nil, err
		}

		if pv.Spec.PersistentVolumeSource.CSI == nil {
			return nil, fmt.Errorf("PV %s is not from CSI driver, cannot take a %s", pv.Name, backup.Spec.Type)
		}

		volumeBackupName := fmt.Sprintf("%s-volume-%s", backup.Name, pvcName)

		vb := v1beta1.VolumeBackup{
			Name:          &volumeBackupName,
			VolumeName:    volumeName,
			CSIDriverName: pv.Spec.PersistentVolumeSource.CSI.Driver,
			PersistentVolumeClaim: v1beta1.PersistentVolumeClaimSourceSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:        pvc.ObjectMeta.Name,
					Namespace:   pvc.ObjectMeta.Namespace,
					Labels:      pvc.Labels,
					Annotations: pvc.Annotations,
				},
				Spec: pvc.Spec,
			},
			ReadyToUse: pointer.BoolPtr(false),
		}

		volumeBackups = append(volumeBackups, vb)
	}

	return volumeBackups, nil
}

func (h *VirtualMachineBackupReconciler) getBackupPVC(namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc := &corev1.PersistentVolumeClaim{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, pvc)
	if err != nil {
		return nil, err
	}

	if pvc.Spec.VolumeName == "" {
		return nil, fmt.Errorf("unbound PVC %s/%s", pvc.Namespace, pvc.Name)
	}

	if pvc.Spec.StorageClassName == nil {
		return nil, fmt.Errorf("no storage class for PVC %s/%s", pvc.Namespace, pvc.Name)
	}

	return pvc, nil
}

// getSecretBackups helps to build a list of SecretBackup upon the secrets used by the backup VM
func (h *VirtualMachineBackupReconciler) getSecretBackups(vm *kubevirtv1.VirtualMachine) ([]v1beta1.SecretBackup, error) {
	secretRefs := []*corev1.LocalObjectReference{}

	for _, credential := range vm.Spec.Template.Spec.AccessCredentials {
		if sshPublicKey := credential.SSHPublicKey; sshPublicKey != nil && sshPublicKey.Source.Secret != nil {
			secretRefs = append(secretRefs, &corev1.LocalObjectReference{
				Name: sshPublicKey.Source.Secret.SecretName,
			})
		}
		if userPassword := credential.UserPassword; userPassword != nil && userPassword.Source.Secret != nil {
			secretRefs = append(secretRefs, &corev1.LocalObjectReference{
				Name: userPassword.Source.Secret.SecretName,
			})
		}
	}

	for _, volume := range vm.Spec.Template.Spec.Volumes {
		if volume.CloudInitNoCloud != nil && volume.CloudInitNoCloud.UserDataSecretRef != nil {
			secretRefs = append(secretRefs, volume.CloudInitNoCloud.UserDataSecretRef)
		}
		if volume.CloudInitNoCloud != nil && volume.CloudInitNoCloud.NetworkDataSecretRef != nil {
			secretRefs = append(secretRefs, volume.CloudInitNoCloud.NetworkDataSecretRef)
		}
	}

	secretBackups := []v1beta1.SecretBackup{}
	secretBackupMap := map[string]bool{}
	for _, secretRef := range secretRefs {
		// users may put SecretRefs in a same secret, so we only keep one
		secretFullName := fmt.Sprintf("%s/%s", vm.Namespace, secretRef.Name)
		if v, ok := secretBackupMap[secretFullName]; ok && v {
			continue
		}

		secretBackup, err := h.getSecretBackupFromSecret(vm.Namespace, secretRef.Name)
		if err != nil {
			return nil, err
		}
		secretBackupMap[secretFullName] = true
		secretBackups = append(secretBackups, *secretBackup)
	}

	return secretBackups, nil
}

func (h *VirtualMachineBackupReconciler) getSecretBackupFromSecret(namespace, name string) (*v1beta1.SecretBackup, error) {
	secret := &corev1.Secret{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: namespace, Name: name}, secret)
	if err != nil {
		return nil, err
	}

	// Remove empty string. If there is empty string in secret, we will encounter error.
	// ref: https://github.com/harvester/harvester/issues/1536
	data := secret.DeepCopy().Data
	for k, v := range secret.Data {
		if len(v) == 0 {
			delete(data, k)
		}
	}

	return &v1beta1.SecretBackup{Name: secret.Name, Data: data}, nil
}

// getCSIDriverMap retrieves VolumeSnapshotClassName for each csi driver
func (h *VirtualMachineBackupReconciler) getCSIDriverMap(backup *v1beta1.VirtualMachineBackup) (map[string]string, map[string]snapshotv1.VolumeSnapshotClass, error) {
	csiDriverConfig := map[string]settings.CSIDriverInfo{}
	if err := json.Unmarshal([]byte(settings.CSIDriverConfig.Get()), &csiDriverConfig); err != nil {
		return nil, nil, fmt.Errorf("unmarshal failed, error: %w, value: %s", err, settings.CSIDriverConfig.Get())
	}

	csiDriverVolumeSnapshotClassNameMap := map[string]string{}
	csiDriverVolumeSnapshotClassMap := map[string]snapshotv1.VolumeSnapshotClass{}
	for _, volumeBackup := range backup.Status.VolumeBackups {
		csiDriverName := volumeBackup.CSIDriverName
		if _, ok := csiDriverVolumeSnapshotClassNameMap[csiDriverName]; ok {
			continue
		}

		if driverInfo, ok := csiDriverConfig[csiDriverName]; !ok {
			return nil, nil, fmt.Errorf("can't find CSI driver %s in setting CSIDriverInfo", csiDriverName)
		} else {
			volumeSnapshotClassName := ""
			switch backup.Spec.Type {
			case v1beta1.Backup:
				volumeSnapshotClassName = driverInfo.BackupVolumeSnapshotClassName
			case v1beta1.Snapshot:
				volumeSnapshotClassName = driverInfo.VolumeSnapshotClassName
			}
			volumeSnapshotClass := &snapshotv1.VolumeSnapshotClass{}
			err := h.Get(context.TODO(), types.NamespacedName{Name: volumeSnapshotClassName}, volumeSnapshotClass)
			if err != nil {
				return nil, nil, fmt.Errorf("can't find volumeSnapshotClass %s for CSI driver %s", volumeSnapshotClassName, csiDriverName)
			}
			csiDriverVolumeSnapshotClassNameMap[csiDriverName] = volumeSnapshotClassName
			csiDriverVolumeSnapshotClassMap[csiDriverName] = *volumeSnapshotClass
		}
	}

	return csiDriverVolumeSnapshotClassNameMap, csiDriverVolumeSnapshotClassMap, nil
}

func volumeToPVCMappings(volumes []kubevirtv1.Volume) map[string]string {
	pvcs := map[string]string{}

	for _, volume := range volumes {
		var pvcName string

		if volume.PersistentVolumeClaim != nil {
			pvcName = volume.PersistentVolumeClaim.ClaimName
		} else {
			continue
		}

		pvcs[volume.Name] = pvcName
	}

	return pvcs
}

// TODO
func (h *VirtualMachineBackupReconciler) GetBackupTargetSetting() string {
	setting := &v1beta1.Setting{}
	h.Get(context.TODO(), types.NamespacedName{Name: settings.BackupTargetSettingName}, setting)
	return setting.Value
}

// SetupWithManager sets up the controller with the Manager.
func (r *VirtualMachineBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.VirtualMachineBackup{}).
		Complete(r)
}
