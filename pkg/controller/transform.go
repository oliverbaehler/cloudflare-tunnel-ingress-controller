package controller

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	"github.com/oliverbaehler/cloudflare-tunnel-ingress-controller/pkg/exposure"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
)

var (
	host string
)

func FromIngressToExposure(ctx context.Context, logger logr.Logger, kubeClient client.Client, ingress networkingv1.Ingress) ([]exposure.Exposure, error) {
	isDeleted := false

	if ingress.DeletionTimestamp != nil {
		isDeleted = true
	}

	if len(ingress.Spec.TLS) > 0 {
		logger.Info("ingress has tls specified, SSL Passthrough is not supported, it will be ignored.")
	}

	var result []exposure.Exposure
	for _, rule := range ingress.Spec.Rules {
		if rule.Host == "" {
			return nil, errors.Errorf("host in ingress %s/%s is empty", ingress.GetNamespace(), ingress.GetName())
		}

		hostname := rule.Host

		scheme := "http"

		if backendProtocol, ok := getAnnotation(ingress.Annotations, AnnotationBackendProtocol); ok {
			scheme = backendProtocol
		}

		cfg, err := annotationProperties(ingress.Annotations)
		if err != nil {
			return nil, errors.Wrap(err, "parse annotation properties")
		}

		for _, path := range rule.HTTP.Paths {
			namespacedName := types.NamespacedName{
				Namespace: ingress.GetNamespace(),
				Name:      path.Backend.Service.Name,
			}
			service := v1.Service{}
			err := kubeClient.Get(ctx, namespacedName, &service)
			if err != nil {
				return nil, errors.Wrapf(err, "fetch service %s", namespacedName)
			}

			// Consider External Service
			if service.Spec.ExternalName != "" {
				host = service.Spec.ExternalName
			} else {
				if service.Spec.ClusterIP == "" {
					return nil, errors.Errorf("service %s has no cluster ip", namespacedName)
				}

				if service.Spec.ClusterIP == "None" {
					return nil, errors.Errorf("service %s has None for cluster ip, headless service is not supported", namespacedName)
				}

				host = service.Spec.ClusterIP
			}

			var port int32
			if path.Backend.Service.Port.Name != "" {
				ok, extractedPort := getPortWithName(service.Spec.Ports, path.Backend.Service.Port.Name)
				if !ok {
					return nil, errors.Errorf("service %s has no port named %s", namespacedName, path.Backend.Service.Port.Name)
				}
				port = extractedPort
			} else {
				port = path.Backend.Service.Port.Number
			}

			// TODO: support other path types
			if path.PathType == nil {
				return nil, errors.Errorf("path type in ingress %s/%s is nil", ingress.GetNamespace(), ingress.GetName())
			}
			if *path.PathType != networkingv1.PathTypePrefix {
				return nil, errors.Errorf("path type in ingress %s/%s is %s, which is not supported", ingress.GetNamespace(), ingress.GetName(), *path.PathType)
			}

			pathPrefix := path.Path

			result = append(result, exposure.Exposure{
				Hostname:      hostname,
				ServiceTarget: fmt.Sprintf("%s://%s:%d", scheme, host, port),
				PathPrefix:    pathPrefix,
				IsDeleted:     isDeleted,
				Config:        *cfg,
			})
		}
	}

	return result, nil
}

func annotationProperties(annotations map[string]string) (*exposure.ExposureConfig, error) {
	config := &exposure.ExposureConfig{}

	for key, value := range annotations {
		switch key {
		case AnnotationProxySSLVerify:
			enabled, err := evalAnnotationBool(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.ProxySSLVerifyEnabled = enabled

		case AnnotationTLSTimeout:
			tlsTimeout, err := stringToCloudflareTime(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.TLSTimeout = tlsTimeout

		case AnnotationConnectionTimeount:
			connectTimeout, err := stringToCloudflareTime(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.ConnectTimeout = connectTimeout

		case AnnotationChunkedEncoding:
			enabled, err := evalAnnotationBool(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.DisableChunkedEncoding = enabled

		case AnnotationHTTP20Origin:
			enabled, err := evalAnnotationBool(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.HTTP2Origin = enabled

		case AnnotationTCPKeepAliveTimeout:
			tunnelDuration, err := stringToCloudflareTime(value)
			if err != nil {
				return nil, errors.Wrapf(err, "parsing annotation value (%s)", key)
			}
			config.KeepAliveTimeout = tunnelDuration

		}
	}

	return config, nil
}
