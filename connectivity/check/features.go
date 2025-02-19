// SPDX-License-Identifier: Apache-2.0
// Copyright 2022 Authors of Cilium

package check

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cilium/cilium/api/v1/models"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cilium/cilium-cli/defaults"
	"github.com/cilium/cilium-cli/k8s"
)

// Feature is the name of a Cilium feature (e.g. l7-proxy, cni chaining mode etc)
type Feature string

const (
	FeatureCNIChaining        Feature = "cni-chaining"
	FeatureMonitorAggregation Feature = "monitor-aggregation"
	FeatureL7Proxy            Feature = "l7-proxy"
	FeatureHostFirewall       Feature = "host-firewall"
)

// FeatureStatus describes the status of a feature. Some features are either
// turned on or off (c.f. Enabled), while others additionally might include a
// Mode string which provides more information about in what mode a
// particular feature is running ((e.g. when running with CNI chaining,
// Enabled will be true, and the Mode string will additionally contain the name
// of the chained CNI).
type FeatureStatus struct {
	Enabled bool
	Mode    string
}

func (s FeatureStatus) String() string {
	str := "Disabled"
	if s.Enabled {
		str = "Enabled"
	}

	if len(s.Mode) == 0 {
		return str
	}

	return fmt.Sprintf("%s:%s", str, s.Mode)
}

// FeatureSet contains the status
type FeatureSet map[Feature]FeatureStatus

// MatchRequirements returns true if the FeatureSet fs satisfies all the
// requirements in reqs. Returns true for empty requirements list.
func (fs FeatureSet) MatchRequirements(reqs ...FeatureRequirement) bool {
	for _, req := range reqs {
		status := fs[req.feature]
		if req.requiresEnabled && (req.enabled != status.Enabled) {
			return false
		}
		if req.requiresMode && (req.mode != status.Mode) {
			return false
		}
	}

	return true
}

// FeatureRequirement defines a test requirement. A given FeatureSet may or
// may not satisfy this requirement
type FeatureRequirement struct {
	feature Feature

	requiresEnabled bool
	enabled         bool

	requiresMode bool
	mode         string
}

// RequireFeatureEnabled constructs a FeatureRequirement which expects the
// feature to be enabled
func RequireFeatureEnabled(feature Feature) FeatureRequirement {
	return FeatureRequirement{
		feature:         feature,
		requiresEnabled: true,
		enabled:         true,
	}
}

// RequireFeatureMode constructs a FeatureRequirement which expects the feature
// to be in the given mode
func RequireFeatureMode(feature Feature, mode string) FeatureRequirement {
	return FeatureRequirement{
		feature:      feature,
		requiresMode: true,
		mode:         mode,
	}
}

func parseBoolStatus(s string) bool {
	switch s {
	case "Enabled", "enabled", "True", "true":
		return true
	}

	return false
}

func (ct *ConnectivityTest) extractFeaturesFromConfigMap(ctx context.Context, client *k8s.Client, result FeatureSet) error {
	cm, err := client.GetConfigMap(ctx, ct.params.CiliumNamespace, defaults.ConfigMapName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("unable to retrieve ConfigMap %q: %w", defaults.ConfigMapName, err)
	}
	if cm.Data == nil {
		return fmt.Errorf("ConfigMap %q does not contain any configuration", defaults.ConfigMapName)
	}

	// Monitor aggregation level defaults to none.
	mode := "none"
	if v, ok := cm.Data["monitor-aggregation"]; ok {
		mode = strings.ToLower(v)
	}
	result[FeatureMonitorAggregation] = FeatureStatus{
		Enabled: mode != "none",
		Mode:    mode,
	}

	return nil
}

func (ct *ConnectivityTest) extractFeaturesFromCiliumStatus(ctx context.Context, ciliumPod Pod, result FeatureSet) error {
	stdout, err := ciliumPod.K8sClient.ExecInPodWithTTY(ctx, ciliumPod.Pod.Namespace, ciliumPod.Pod.Name,
		defaults.AgentContainerName, []string{"cilium", "status", "-o", "json"})
	if err != nil {
		return fmt.Errorf("failed to fetch cilium status: %w", err)
	}

	st := &models.StatusResponse{}
	if err := json.Unmarshal(stdout.Bytes(), st); err != nil {
		return fmt.Errorf("unmarshaling Cilium stdout json: %w", err)
	}

	// CNI chaining
	mode := ""
	if st.CniChaining != nil {
		mode = st.CniChaining.Mode
	}
	result[FeatureCNIChaining] = FeatureStatus{
		Enabled: mode != "",
		Mode:    mode,
	}

	// L7 Proxy
	result[FeatureL7Proxy] = FeatureStatus{
		Enabled: st.Proxy != nil,
	}

	// Host Firewall
	status := false
	if hf := st.HostFirewall; hf != nil {
		status = parseBoolStatus(st.HostFirewall.Mode)
	}
	result[FeatureHostFirewall] = FeatureStatus{
		Enabled: status,
	}

	return nil
}

func (ct *ConnectivityTest) validateFeatureSet(other FeatureSet, source string) {
	for key, found := range other {
		expected, ok := ct.features[key]
		if !ok {
			ct.Warnf("Cilium feature %q found in pod %s, but not in reference set", key, source)
		} else {
			if expected != found {
				ct.Warnf("Cilium feature %q differs in pod %s. Expected %q, found %q", key, source, expected, found)
			}
		}
	}

	for key := range ct.features {
		if _, ok := other[key]; !ok {
			ct.Warnf("Cilium feature %q not found in pod %s", key, source)
		}
	}
}

func (ct *ConnectivityTest) detectFeatures(ctx context.Context) error {
	initialized := false
	for _, ciliumPod := range ct.ciliumPods {
		features := FeatureSet{}

		err := ct.extractFeaturesFromConfigMap(ctx, ciliumPod.K8sClient, features)
		if err != nil {
			return err
		}
		err = ct.extractFeaturesFromCiliumStatus(ctx, ciliumPod, features)
		if err != nil {
			return err
		}

		if initialized {
			ct.validateFeatureSet(features, ciliumPod.Name())
		} else {
			ct.features = features
			initialized = true
		}
	}

	return nil
}
