package controller

import (
	"context"
	"os"

	cloudflarecontroller "github.com/oliverbaehler/cloudflare-tunnel-ingress-controller/pkg/cloudflare-controller"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func CreateControlledCloudflaredIfNotExist(
	ctx context.Context,
	kubeClient client.Client,
	tunnelClient *cloudflarecontroller.TunnelClient,
	namespace string,
) error {
	list := appsv1.DeploymentList{}
	err := kubeClient.List(ctx, &list, &client.ListOptions{
		Namespace: namespace,
		LabelSelector: labels.SelectorFromSet(labels.Set{
			"strrl.dev/cloudflare-tunnel-ingress-controller": "controlled-cloudflared-connector",
		}),
	})
	if err != nil {
		return errors.Wrapf(err, "list controlled-cloudflared-connector in namespace %s", namespace)
	}

	// Template Deployment is always needed
	token, err := tunnelClient.FetchTunnelToken(ctx)
	if err != nil {
		return errors.Wrap(err, "fetch tunnel token")
	}

	deployment := cloudflaredConnectDeploymentTemplating(token, namespace)

	// When a tunnel is found, compare if the current is deployed
	if len(list.Items) > 0 {
		err := kubeClient.Update(ctx, deployment)
		if err != nil {
			return errors.Wrap(err, "update controlled-cloudflared-connector deployment")
		}
	} else {
		err = kubeClient.Create(ctx, deployment)
		if err != nil {
			return errors.Wrap(err, "create controlled-cloudflared-connector deployment")
		}
	}

	return nil
}

func cloudflaredConnectDeploymentTemplating(token string, namespace string) *appsv1.Deployment {
	appName := "controlled-cloudflared-connector"
	image := os.Getenv("CLOUDFLARED_IMAGE")
	pullPolicy := os.Getenv("CLOUDFLARED_IMAGE_PULL_POLICY")
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      appName,
			Namespace: namespace,
			Labels: map[string]string{
				"app": appName,
				"strrl.dev/cloudflare-tunnel-ingress-controller": "controlled-cloudflared-connector",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: pointer.Int32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": appName,
				},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name: appName,
					Labels: map[string]string{
						"app": appName,
					},
				},
				Spec: v1.PodSpec{
					SecurityContext: &v1.PodSecurityContext{
						RunAsNonRoot: pointer.Bool(true),
						FSGroup:      pointer.Int64(65532),
					},
					Containers: []v1.Container{
						{
							Name:            appName,
							Image:           image,
							ImagePullPolicy: v1.PullPolicy(pullPolicy),
							Command: []string{
								"cloudflared",
								"--no-autoupdate",
								"tunnel",
								"--metrics",
								"0.0.0.0:44483",
								"run",
								"--token",
								token,
							},
							SecurityContext: &v1.SecurityContext{
								RunAsUser:                pointer.Int64(65532),
								RunAsGroup:               pointer.Int64(65532),
								ReadOnlyRootFilesystem:   pointer.Bool(true),
								AllowPrivilegeEscalation: pointer.Bool(false), // Prevent privilege escalation
								Capabilities: &v1.Capabilities{
									Drop: []v1.Capability{
										"ALL", // Drop all capabilities
									},
								},
							},
							Ports: []v1.ContainerPort{
								{
									Name:          "metrics",
									ContainerPort: 44483,
									Protocol:      v1.ProtocolTCP,
								},
							},
						},
					},
					RestartPolicy: v1.RestartPolicyAlways,
				},
			},
		},
	}
}
