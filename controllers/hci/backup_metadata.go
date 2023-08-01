package hci

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/longhorn/backupstore"
	_ "github.com/longhorn/backupstore/nfs"
	_ "github.com/longhorn/backupstore/s3"
	"github.com/sirupsen/logrus"
	"github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/settings"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"os"
	"path/filepath"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	metadataFolderPath           = "hci/vmbackups/"
	backupMetadataControllerName = "hci-backup-metadata-controller"
)

type VirtualMachineBackupMetadata struct {
	Name          string                            `json:"name"`
	Namespace     string                            `json:"namespace"`
	BackupSpec    v1beta1.VirtualMachineBackupSpec  `json:"backupSpec,omitempty"`
	VMSourceSpec  *v1beta1.VirtualMachineSourceSpec `json:"vmSourceSpec,omitempty"`
	VolumeBackups []v1beta1.VolumeBackup            `json:"volumeBackups,omitempty"`
	SecretBackups []v1beta1.SecretBackup            `json:"secretBackups,omitempty"`
}

func loadBackupMetadataInBackupTarget(filePath string, bsDriver backupstore.BackupStoreDriver) (*VirtualMachineBackupMetadata, error) {
	if !bsDriver.FileExists(filePath) {
		return nil, fmt.Errorf("cannot find %v in backupstore", filePath)
	}

	rc, err := bsDriver.Read(filePath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	backupMetadata := &VirtualMachineBackupMetadata{}
	if err := json.NewDecoder(rc).Decode(backupMetadata); err != nil {
		return nil, err
	}
	return backupMetadata, nil
}

type MetadataReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=hci.wjyl.com,resources=settings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=settings/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=hci.wjyl.com,resources=settings/finalizers,verbs=update

func (h *MetadataReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	setting := &v1beta1.Setting{}
	if err := h.Get(ctx, req.NamespacedName, setting); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return h.OnBackupTargetChange(req.String(), setting)
}

// OnBackupTargetChange resync vm metadata files when backup target change
func (h *MetadataReconciler) OnBackupTargetChange(key string, setting *v1beta1.Setting) (ctrl.Result, error) {
	if setting == nil || setting.DeletionTimestamp != nil ||
		setting.Name != settings.BackupTargetSettingName || setting.Value == "" {
		return ctrl.Result{}, nil
	}

	target, err := settings.DecodeBackupTarget(setting.Value)
	if err != nil {
		return ctrl.Result{}, err
	}

	logrus.Debugf("backup target change, sync vm backup:%s:%s", target.Type, target.Endpoint)

	// when backup target is reset to default, do not trig sync
	if target.IsDefaultBackupTarget() {
		return ctrl.Result{}, nil
	}

	if err = h.syncVMBackup(target); err != nil {
		logrus.Errorf("can't sync vm backup metadata, target:%s:%s, err: %v", target.Type, target.Endpoint, err)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (h *MetadataReconciler) syncVMBackup(target *settings.BackupTarget) error {
	if target.Type == settings.S3BackupType {
		secret := &corev1.Secret{}
		err := h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: util.BackupTargetSecretName}, secret)
		if err != nil {
			return err
		}
		os.Setenv(AWSAccessKey, string(secret.Data[AWSAccessKey]))
		os.Setenv(AWSSecretKey, string(secret.Data[AWSSecretKey]))
		os.Setenv(AWSEndpoints, string(secret.Data[AWSEndpoints]))
		os.Setenv(AWSCERT, string(secret.Data[AWSCERT]))
	}

	endpoint := ConstructEndpoint(target)
	bsDriver, err := backupstore.GetBackupStoreDriver(endpoint)
	if err != nil {
		return err
	}

	fileNames, err := bsDriver.List(filepath.Join(metadataFolderPath))
	if err != nil {
		return err
	}

	for _, fileName := range fileNames {
		backupMetadata, err := loadBackupMetadataInBackupTarget(filepath.Join(metadataFolderPath, fileName), bsDriver)
		if err != nil {
			return err
		}
		if backupMetadata.Namespace == "" {
			backupMetadata.Namespace = metav1.NamespaceDefault
		}
		if err := h.createVMBackupIfNotExist(*backupMetadata, target); err != nil {
			return err
		}
	}
	return nil
}

func (h *MetadataReconciler) createVMBackupIfNotExist(backupMetadata VirtualMachineBackupMetadata, target *settings.BackupTarget) error {
	vmBackup := &v1beta1.VirtualMachineBackup{}
	if err := h.Get(context.TODO(), types.NamespacedName{Namespace: backupMetadata.Namespace, Name: backupMetadata.Name}, vmBackup); err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if err == nil {
		return nil
	}

	if err := h.createNamespaceIfNotExist(backupMetadata.Namespace); err != nil {
		return err
	}
	if err := h.Create(context.TODO(), &v1beta1.VirtualMachineBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupMetadata.Name,
			Namespace: backupMetadata.Namespace,
		},
		Spec: backupMetadata.BackupSpec,
		Status: &v1beta1.VirtualMachineBackupStatus{
			ReadyToUse: pointer.BoolPtr(false),
			BackupTarget: &v1beta1.BackupTarget{
				Endpoint:     target.Endpoint,
				BucketName:   target.BucketName,
				BucketRegion: target.BucketRegion,
			},
			SourceSpec:    backupMetadata.VMSourceSpec,
			VolumeBackups: backupMetadata.VolumeBackups,
			SecretBackups: backupMetadata.SecretBackups,
		},
	}); err != nil {
		return err
	}
	return nil
}

func (h *MetadataReconciler) createNamespaceIfNotExist(namespace string) error {
	ns := &corev1.Namespace{}
	if err := h.Get(context.TODO(), types.NamespacedName{Name: namespace}, ns); err != nil && !apierrors.IsNotFound(err) {
		return err
	} else if err == nil {
		return nil
	}

	err := h.Create(context.TODO(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	})
	return err
}

func (h *MetadataReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Setting{}).
		Complete(h)
}
