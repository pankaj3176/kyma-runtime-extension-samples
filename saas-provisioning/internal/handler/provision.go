package handler

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"path"
	"reflect"
	"runtime"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"encoding/base64"
	"encoding/json"

	"github.com/google/uuid"
	apigatewayv1alpha1 "github.com/kyma-incubator/api-gateway/api/v1alpha1"
	rulev1alpha1 "github.com/ory/oathkeeper-maester/api/v1alpha1"
	log "github.com/sirupsen/logrus"
)

func (c *Config) ProvisionTenent() error {

	var err error
	err = c.createRouteResource()
	if err != nil {
		log.Error(err)
	}

	err = c.createAppAuthProxy()
	if err != nil {
		log.Error(err)
	}

	return err
}

func (c *Config) createRouteResource() error {

	var err error
	for _, s := range c.AppConfig.AppAuthProxy.Routes {
		fmt.Println("Looking for K8 config for Target: ", s.Target)

		var appName string
		if len(s.K8Config.Image) != 0 {

			imageAndVersion := s.K8Config.Image[strings.LastIndex(s.K8Config.Image, "/")+1:]
			imageOnly := strings.Split(imageAndVersion, ":")[0]
			appName = imageOnly + "-" + c.Tenant

			log.Infof("Found Image - creating k8s resources: %s", appName)

			volumeMounts := []apiv1.VolumeMount{}
			var vm apiv1.VolumeMount
			if len(s.K8Config.VolumeMounts) > 0 {
				for _, s := range s.K8Config.VolumeMounts {
					vm.Name = s.Name
					vm.MountPath = s.MountPath
					vm.SubPath = s.SubPath
					volumeMounts = append(volumeMounts, vm)
				}
			}

			volumes := []apiv1.Volume{}
			var v apiv1.Volume
			var cmName string

			if len(s.K8Config.Volumes) > 0 {
				for i, s := range s.K8Config.Volumes {
					cmName = s.ConfigMap.Name + "-" + c.Tenant + "-" + fmt.Sprint(i)
					v.Name = s.Name
					v.VolumeSource = apiv1.VolumeSource{
						ConfigMap: &apiv1.ConfigMapVolumeSource{
							LocalObjectReference: apiv1.LocalObjectReference{
								Name: cmName,
							},
						},
					}
					volumes = append(volumes, v)

					_, rcpath, _, _ := runtime.Caller(1)
					filePath := path.Join(path.Dir(rcpath), s.ConfigMap.FilePath)
					fileData, err := ioutil.ReadFile(filePath)
					if err == nil {
						//call the FunctionProcessor to process the config map creation otherwise just create it
						//CustomMethodProcessor must be exported!
						if s.ConfigMap.CustomMethodProcessor != "" {
							cmMethProcess := reflect.ValueOf(c).MethodByName(s.ConfigMap.CustomMethodProcessor)
							inputs := make([]reflect.Value, 3)
							inputs[0] = reflect.ValueOf(fileData)
							inputs[1] = reflect.ValueOf(s.ConfigMap.FileKey)
							inputs[2] = reflect.ValueOf(cmName)
							cmMethProcess.Call(inputs)
						} else {
							data := string(fileData)
							cmData := make(map[string]string)
							cmData[s.ConfigMap.FileKey] = data
							c.createConfigMap(cmName, cmData)
						}
					} else {
						log.Error(err)
					}

				}
			}

			err = c.createDeployment(appName, s.K8Config.Image, volumeMounts, volumes)
			if err != nil {
				log.Error(err)
			}

			err = c.createService(appName, s.K8Config.SvcTargetPort)
			if err != nil {
				log.Error(err)
			}
		} else {
			log.Infof("No K8 config found")
		}
	}
	return err
}

func (c *Config) ProcessTemplateForCM(fileData []byte, fileKey string, cmName string) {

	data := string(fileData)

	tmpl := template.New("cm")

	tmpl, err := tmpl.Parse(data)
	if err != nil {
		log.Error(err)
		return
	}

	var tmpData bytes.Buffer
	tmpl.Execute(&tmpData, c.RequestInfo)
	if err != nil {
		log.Error(err)
		return
	}

	cmData := make(map[string]string)
	cmData[fileKey] = tmpData.String()
	c.createConfigMap(cmName, cmData)
}

func (c *Config) createAppAuthProxy() error {

	name := c.AppConfig.AppName + "-" + c.Tenant

	var err error
	data, err := c.createConfigMap_AppAuthProxy()
	err = c.createConfigMap(name, data)
	if err != nil {
		log.Error(err)
	}

	volumeMount := []apiv1.VolumeMount{
		apiv1.VolumeMount{
			Name:      "config-volume",
			MountPath: "/app/config",
		},
	}

	volume := []apiv1.Volume{
		apiv1.Volume{
			Name:         "config-volume",
			VolumeSource: apiv1.VolumeSource{ConfigMap: &apiv1.ConfigMapVolumeSource{LocalObjectReference: apiv1.LocalObjectReference{Name: name}}},
		},
	}

	err = c.createDeployment(name, c.AppConfig.AppAuthProxyImage, volumeMount, volume)
	if err != nil {
		log.Error(err)
	}

	port := c.AppConfig.AppAuthProxySvcTargetPort
	err = c.createService(name, port)
	if err != nil {
		log.Error(err)
	}

	err = c.createAPIRule(name)
	if err != nil {
		log.Error(err)
	}

	return err
}

func (c *Config) createDeployment(name string, image string, volumeMount []apiv1.VolumeMount, volume []apiv1.Volume) error {

	log.Info("Creating deployment...")

	deploymentsClient := c.AppConfig.K8Config.Clientset.AppsV1().Deployments(c.AppConfig.Namespace)
	lbs := map[string]string{"app": name}

	deployment := &appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbs},
		Spec: appsv1.DeploymentSpec{Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: apiv1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: apiv1.PodSpec{Containers: []apiv1.Container{{
					Name:         name,
					Image:        image,
					VolumeMounts: volumeMount,
				}},
					Volumes: volume,
				}}},
		Status: appsv1.DeploymentStatus{},
	}

	// Create Deployment
	result, err := deploymentsClient.Create(context.TODO(), deployment, metav1.CreateOptions{})

	if err != nil {
		return err
	}

	log.Infof("Created deployment %q.\n", result.GetObjectMeta().GetName())
	return nil

}

func (c *Config) createService(name string, port int32) error {

	log.Info("Creating service...")

	serviceClient := c.AppConfig.K8Config.Clientset.CoreV1().Services(c.AppConfig.Namespace)

	lbs := map[string]string{"app": name}

	service := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbs},
		Spec: apiv1.ServiceSpec{
			Selector: lbs,
			Type:     apiv1.ServiceTypeClusterIP,
			Ports: []apiv1.ServicePort{
				{
					Name: "http",
					Port: 80,
					TargetPort: intstr.IntOrString{
						Type:   0,
						IntVal: port,
					},
				},
			},
		},
		Status: apiv1.ServiceStatus{},
	}

	result, err := serviceClient.Create(context.TODO(), service, metav1.CreateOptions{})

	if err != nil {
		return err
	}

	log.Infof("Created service %q.\n", result.GetObjectMeta().GetName())
	return nil

}

func (c *Config) createConfigMap(name string, data map[string]string) error {

	log.Info("Creating Configmap...")

	cmClient := c.AppConfig.K8Config.Clientset.CoreV1().ConfigMaps(c.AppConfig.Namespace)

	lbs := map[string]string{"app": name}

	configMap := &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbs},
		Data:       data,
	}

	result, err := cmClient.Create(context.TODO(), configMap, metav1.CreateOptions{})

	if err != nil {
		return err
	}

	log.Infof("Created Configmap %q.\n", result.GetObjectMeta().GetName())
	return nil
}

func (c *Config) createAPIRule(name string) error {

	log.Info("Creating APIRule...")

	apiRule := getAPIRule(name, c.AppConfig.Namespace)

	err := c.AppConfig.K8Config.APIRuleClientset.Create(context.TODO(), apiRule)

	if err != nil {
		return err
	}

	log.Infof("Created APIRule %s", name)

	return nil
}

func int32Ptr(i int32) *int32 { return &i }

func (c *Config) createConfigMap_AppAuthProxy() (map[string]string, error) {

	data := c.AppConfig.AppAuthProxy
	data.Routes = c.AppConfig.AppAuthProxy.Routes

	cmData := make(map[string]string)

	data.Cookie.Key = uuid.New().String()

	data.RedirectURI = "https://" + c.AppConfig.AppName + "-" + c.Tenant + "." + c.AppConfig.Domain + "/oauth/callback"
	data.IDPConfig.URL = c.RequestInfo.AdditionalInformation.TokenURL
	data.IDPConfig.ClientSecret = c.RequestInfo.AdditionalInformation.ClientSecret
	data.IDPConfig.ClientID = c.RequestInfo.AdditionalInformation.ClientID

	for i, s := range data.Routes {
		if len(s.K8Config.Image) != 0 {
			data.Routes[i].Target = s.Target + "-" + c.Tenant + "." + c.AppConfig.Namespace
		}
	}

	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	cmData["config.json"] = string(b)

	return cmData, nil
}

func encodeToBase64(v interface{}) (string, error) {
	var buf bytes.Buffer
	encoder := base64.NewEncoder(base64.StdEncoding, &buf)
	err := json.NewEncoder(encoder).Encode(v)
	if err != nil {
		return "", err
	}
	encoder.Close()
	return buf.String(), nil
}

func getAPIRule(name string, namespace string) *apigatewayv1alpha1.APIRule {
	gateway := "kyma-gateway.kyma-system.svc.cluster.local"
	port := uint32(80)

	lbs := map[string]string{"app": name}

	handler := &rulev1alpha1.Handler{
		Name: "noop",
	}

	rule := apigatewayv1alpha1.Rule{
		Path: "/.*",
		Methods: []string{
			http.MethodPost,
			http.MethodGet,
			http.MethodDelete,
			http.MethodPost,
			http.MethodPatch,
			http.MethodHead,
		},
		AccessStrategies: []*rulev1alpha1.Authenticator{
			{
				Handler: handler,
			},
		},
	}

	apiRule := &apigatewayv1alpha1.APIRule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbs, Namespace: namespace},
		Spec: apigatewayv1alpha1.APIRuleSpec{
			Service: &apigatewayv1alpha1.Service{
				Name: &name,
				Port: &port,
				Host: &name,
			},
			Gateway: &gateway,
			Rules:   []apigatewayv1alpha1.Rule{rule},
		},
	}

	return apiRule
}
