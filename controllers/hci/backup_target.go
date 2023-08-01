package hci

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/wangpan-hqu/hci-controller-manager/apis/hci/v1beta1"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/util"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strconv"
	"strings"

	longhornv1 "github.com/longhorn/longhorn-manager/k8s/pkg/apis/longhorn/v1beta1"
	"github.com/wangpan-hqu/hci-controller-manager/pkg/settings"
)

const (
	backupTargetControllerName = "harvester-backup-target-controller"

	longhornBackupTargetSettingName       = "backup-target"
	longhornBackupTargetSecretSettingName = "backup-target-credential-secret"

	AWSAccessKey       = "AWS_ACCESS_KEY_ID"
	AWSSecretKey       = "AWS_SECRET_ACCESS_KEY"
	AWSEndpoints       = "AWS_ENDPOINTS"
	AWSCERT            = "AWS_CERT"
	VirtualHostedStyle = "VIRTUAL_HOSTED_STYLE"
)

// BackupSettingReconciler reconciles a Setting object of backupTarget
type BackupSettingReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="longhorn.io",resources=settings,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups=longhorn.io,resources=settings,verbs=get;list;watch;create;update;delete

func (h *BackupSettingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	logrus.Info("get backup: " + req.String())
	setting := &v1beta1.Setting{}
	if err := h.Get(ctx, req.NamespacedName, setting); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return h.OnChange(setting)
}

// OnBackupChange handles setting object on change
func (h *BackupSettingReconciler) OnChange(setting *v1beta1.Setting) (ctrl.Result, error) {
	if setting == nil || setting.DeletionTimestamp != nil ||
		setting.Name != settings.BackupTargetSettingName || setting.Value == "" {
		return ctrl.Result{}, nil
	}

	target, err := settings.DecodeBackupTarget(setting.Value)
	if err != nil {
		return h.setConfiguredCondition(setting, "", err)
	}

	logrus.Debugf("backup target change:%s:%s", target.Type, target.Endpoint)

	switch target.Type {
	case settings.S3BackupType:
		// Since S3 access key id and secret access key are stripped after S3 backup target has been verified
		// in reUpdateBackupTargetSettingSecret
		// stop the controller to reconcile it
		if target.SecretAccessKey == "" && target.AccessKeyID == "" {
			break
		}

		if err = h.updateLonghornTarget(target); err != nil {
			return h.setConfiguredCondition(setting, "", err)
		}

		if err = h.updateBackupTargetSecret(target); err != nil {
			return h.setConfiguredCondition(setting, "", err)
		}

		return h.reUpdateBackupTargetSettingSecret(setting, target)

	case settings.NFSBackupType:
		if err = h.updateLonghornTarget(target); err != nil {
			return h.setConfiguredCondition(setting, "", err)
		}

		// delete the may existing previous secret of S3
		if err = h.deleteBackupTargetSecret(target); err != nil {
			return h.setConfiguredCondition(setting, "", err)
		}

	default:
		// reset backup target to default, then delete/update related settings
		if target.IsDefaultBackupTarget() {
			if err = h.updateLonghornTarget(target); err != nil {
				return h.setConfiguredCondition(setting, "", err)
			}

			// delete the may existing previous secret of S3
			if err = h.deleteBackupTargetSecret(target); err != nil {
				return h.setConfiguredCondition(setting, "", err)
			}

			settingCpy := setting.DeepCopy()
			v1beta1.SettingConfigured.False(settingCpy)
			v1beta1.SettingConfigured.Message(settingCpy, "")
			v1beta1.SettingConfigured.Reason(settingCpy, "")
			err = h.Update(context.TODO(), settingCpy)
			return ctrl.Result{}, err
		}

		return h.setConfiguredCondition(setting, "", fmt.Errorf("Invalid backup target type:%s or parameter", target.Type))
	}

	if len(setting.Status.Conditions) == 0 || v1beta1.SettingConfigured.IsFalse(setting) {
		return h.setConfiguredCondition(setting, "", nil)
	}
	return ctrl.Result{}, nil
}

func (h *BackupSettingReconciler) reUpdateBackupTargetSettingSecret(setting *v1beta1.Setting, target *settings.BackupTarget) (ctrl.Result, error) {
	// only do a second update when s3 with credentials
	if target.Type != settings.S3BackupType {
		return ctrl.Result{}, nil
	}

	// reset the s3 credentials to prevent controller reconcile and not to expose secret key
	target.SecretAccessKey = ""
	target.AccessKeyID = ""
	targetBytes, err := json.Marshal(target)
	if err != nil {
		return ctrl.Result{}, err
	}

	settingCpy := setting.DeepCopy()
	settingCpy.Value = string(targetBytes)

	err = h.Update(context.TODO(), settingCpy)
	return ctrl.Result{}, err
}

func (h *BackupSettingReconciler) updateLonghornTarget(backupTarget *settings.BackupTarget) error {
	target := &longhornv1.Setting{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: longhornBackupTargetSettingName}, target)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		if err := h.Create(context.TODO(), &longhornv1.Setting{
			ObjectMeta: metav1.ObjectMeta{
				Name:      longhornBackupTargetSettingName,
				Namespace: util.LonghornSystemNamespaceName,
			},
			Value: ConstructEndpoint(backupTarget),
		}); err != nil {
			return err
		}
		return nil
	}

	targetCpy := target.DeepCopy()
	targetCpy.Value = ConstructEndpoint(backupTarget)

	if !reflect.DeepEqual(target, targetCpy) {
		err := h.Update(context.TODO(), targetCpy)
		return err
	}
	return nil
}

func getBackupSecretData(target *settings.BackupTarget) (map[string]string, error) {
	data := map[string]string{
		AWSAccessKey:       target.AccessKeyID,
		AWSSecretKey:       target.SecretAccessKey,
		AWSEndpoints:       target.Endpoint,
		AWSCERT:            target.Cert,
		VirtualHostedStyle: strconv.FormatBool(target.VirtualHostedStyle),
	}
	if settings.AdditionalCA.Get() != "" {
		data[AWSCERT] = settings.AdditionalCA.Get()
	}

	var httpProxyConfig util.HTTPProxyConfig
	if err := json.Unmarshal([]byte(settings.HTTPProxy.Get()), &httpProxyConfig); err != nil {
		return nil, err
	}
	data[util.HTTPProxyEnv] = httpProxyConfig.HTTPProxy
	data[util.HTTPSProxyEnv] = httpProxyConfig.HTTPSProxy
	data[util.NoProxyEnv] = util.AddBuiltInNoProxy(httpProxyConfig.NoProxy)

	return data, nil
}

func (h *BackupSettingReconciler) updateBackupTargetSecret(target *settings.BackupTarget) error {
	backupSecretData, err := getBackupSecretData(target)
	if err != nil {
		return err
	}

	secret := &corev1.Secret{}
	err = h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: util.BackupTargetSecretName}, secret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		newSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      util.BackupTargetSecretName,
				Namespace: util.LonghornSystemNamespaceName,
			},
		}
		newSecret.StringData = backupSecretData
		if err = h.Create(context.TODO(), newSecret); err != nil {
			return err
		}
	} else {
		secretCpy := secret.DeepCopy()
		secretCpy.StringData = backupSecretData
		if !reflect.DeepEqual(secret.StringData, secretCpy.StringData) {
			if err := h.Update(context.TODO(), secretCpy); err != nil {
				return err
			}
		}
	}

	return h.updateLonghornBackupTargetSecretSetting(target)
}

func (h *BackupSettingReconciler) deleteBackupTargetSecret(target *settings.BackupTarget) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: util.LonghornSystemNamespaceName,
			Name:      util.BackupTargetSecretName,
		},
	}
	if err := h.Delete(context.TODO(), secret); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	setting := &longhornv1.Setting{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: util.LonghornSystemNamespaceName,
			Name:      longhornBackupTargetSecretSettingName,
		},
	}
	if err := h.Delete(context.TODO(), setting); err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	return nil
}

func (h *BackupSettingReconciler) updateLonghornBackupTargetSecretSetting(target *settings.BackupTarget) error {
	targetSecret := &longhornv1.Setting{}
	err := h.Get(context.TODO(), types.NamespacedName{Namespace: util.LonghornSystemNamespaceName, Name: longhornBackupTargetSecretSettingName}, targetSecret)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}

		if err := h.Create(context.TODO(), &longhornv1.Setting{
			ObjectMeta: metav1.ObjectMeta{
				Name:      longhornBackupTargetSecretSettingName,
				Namespace: util.LonghornSystemNamespaceName,
			},
			Value: util.BackupTargetSecretName,
		}); err != nil {
			return err
		}
		return nil
	}

	targetSecCpy := targetSecret.DeepCopy()
	targetSecCpy.Value = util.BackupTargetSecretName

	if targetSecret.Value != targetSecCpy.Value {
		if err := h.Update(context.TODO(), targetSecCpy); err != nil {
			return err
		}
	}

	return nil
}

func (h *BackupSettingReconciler) setConfiguredCondition(setting *v1beta1.Setting, reason string, err error) (ctrl.Result, error) {
	settingCpy := setting.DeepCopy()
	// SetError with nil error will cleanup message in condition and set the status to true
	v1beta1.SettingConfigured.SetError(settingCpy, reason, err)
	err2 := h.Update(context.TODO(), settingCpy)
	return ctrl.Result{}, err2
}

func ConstructEndpoint(target *settings.BackupTarget) string {
	switch target.Type {
	case settings.S3BackupType:
		return fmt.Sprintf("s3://%s@%s/", target.BucketName, target.BucketRegion)
	case settings.NFSBackupType:
		// we allow users to input nfs:// prefix as optional
		return fmt.Sprintf("nfs://%s", strings.TrimPrefix(target.Endpoint, "nfs://"))
	default:
		return target.Endpoint
	}
}

func (h *BackupSettingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Setting{}).
		Complete(h)
}
