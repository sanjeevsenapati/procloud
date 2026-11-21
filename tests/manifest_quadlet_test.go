package tests

import (
	"strings"
	"testing"

	"procloud.io/procloud/internal/manifest"
	"procloud.io/procloud/pkg/quadlet"
)

func TestManifestValidationAndQuadletGeneration(t *testing.T) {
	manifestYAML := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: payment-service
  project: finance
spec:
  replicas: 1
  image:
    repository: quay.io/acme/payment
    tag: 1.2.0
  ports:
    - containerPort: 8080
      hostPort: 8080
      protocol: TCP
      ingress:
        enabled: true
        domain: pay.acme.local
        path: /
  env:
    - name: ENV
      value: production
    - name: LOG_LEVEL
      value: info
  volumes:
    - name: pay-data
      mountPath: /var/data
      size: 10Gi
  resources:
    limits:
      cpu: "2.0"
      memory: "1Gi"
    requests:
      cpu: "0.5"
      memory: "256Mi"
  healthCheck:
    httpGet:
      path: /healthz
      port: 8080
    periodSeconds: 10
  restartPolicy: always
`)

	m, err := manifest.ParseAndValidate(manifestYAML)
	if err != nil {
		t.Fatalf("failed to parse manifest: %v", err)
	}

	if m.Metadata.Name != "payment-service" {
		t.Errorf("expected name payment-service, got %s", m.Metadata.Name)
	}
	if len(m.Spec.Ports) != 1 || m.Spec.Ports[0].ContainerPort != 8080 {
		t.Errorf("unexpected port spec")
	}

	// Generate Quadlet
	hostVols := map[string]string{
		"pay-data": "/var/lib/procloud/volumes/finance/pay-data",
	}
	quadletCfg := m.ToQuadletContainer("acme-net", hostVols)
	unitText := quadlet.GenerateContainerUnit(quadletCfg)

	if !strings.Contains(unitText, "[Container]") {
		t.Errorf("quadlet missing [Container] header")
	}
	if !strings.Contains(unitText, "Image=quay.io/acme/payment:1.2.0") {
		t.Errorf("quadlet missing image specification")
	}
	if !strings.Contains(unitText, "PublishPort=8080:8080/tcp") {
		t.Errorf("quadlet missing publish port")
	}
	if !strings.Contains(unitText, "Volume=/var/lib/procloud/volumes/finance/pay-data:/var/data:Z") {
		t.Errorf("quadlet missing volume mount with SELinux Z label")
	}
	if !strings.Contains(unitText, "Network=acme-net") {
		t.Errorf("quadlet missing network")
	}
	if !strings.Contains(unitText, "HealthCmd=curl -f http://localhost:8080/healthz || exit 1") {
		t.Errorf("quadlet missing health check command")
	}
	if !strings.Contains(unitText, "WantedBy=default.target") {
		t.Errorf("quadlet missing install target")
	}
}
