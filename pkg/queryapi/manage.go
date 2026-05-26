/*
Copyright 2026.

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

package queryapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	scalingv1alpha1 "github.com/Ngoga-Musagi/agentic-autoscaler/api/v1alpha1"
)

// ConsoleConfig controls the management and load-generation features of the
// console. Mutating endpoints are disabled by default, so the console is safe
// to expose read-only; enabling WriteEnabled lets it create, edit, and delete
// AgenticAutoscaler CRs cluster-wide and drive the load generator.
type ConsoleConfig struct {
	// WriteEnabled allows the mutating endpoints (POST/PATCH/DELETE on
	// autoscalers and the load-control proxy). Default false.
	WriteEnabled bool
	// AuthToken, when non-empty, must be presented as a Bearer token on every
	// mutating request.
	AuthToken string
	// LoadgenURL is the base URL of the load generator control API. Empty hides
	// the Load tab and disables the /api/load proxy.
	LoadgenURL string
}

// ---- shared response shapes -------------------------------------------------

type consoleConfigResponse struct {
	WriteEnabled   bool `json:"writeEnabled"`
	AuthRequired   bool `json:"authRequired"`
	LoadgenEnabled bool `json:"loadgenEnabled"`
}

type deploymentInfo struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Replicas  int32  `json:"replicas"`
	Managed   bool   `json:"managed"`
	HasHPA    bool   `json:"hasHPA"`
}

type autoscalerSummary struct {
	Namespace          string       `json:"namespace"`
	Name               string       `json:"name"`
	TargetDeployment   string       `json:"targetDeployment"`
	TargetNamespace    string       `json:"targetNamespace"`
	MinReplicas        int32        `json:"minReplicas"`
	MaxReplicas        int32        `json:"maxReplicas"`
	CurrentReplicas    int32        `json:"currentReplicas"`
	DryRun             bool         `json:"dryRun"`
	Mode               string       `json:"mode"`
	Provider           string       `json:"provider"`
	LastDecisionReason string       `json:"lastDecisionReason,omitempty"`
	LastScaleTime      *metav1.Time `json:"lastScaleTime,omitempty"`
	CoexistenceStatus  string       `json:"coexistenceStatus,omitempty"`
}

// patchRequest is the JSON body for PATCH /api/autoscalers/{ns}/{name}. Only the
// fields a console user is allowed to flip at runtime are exposed; all are
// optional so the UI can send just what changed.
type patchRequest struct {
	DryRun      *bool  `json:"dryRun,omitempty"`
	MinReplicas *int32 `json:"minReplicas,omitempty"`
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`
}

// ---- auth -------------------------------------------------------------------

// requireWrite enforces the write-mode gate and optional bearer token. It writes
// the appropriate error response and returns false when the request must be
// rejected.
func (s *Server) requireWrite(w http.ResponseWriter, r *http.Request) bool {
	if !s.cfg.WriteEnabled {
		jsonError(w, "write mode disabled; set CONSOLE_WRITE_ENABLED=true to allow cluster changes", http.StatusForbidden)
		return false
	}
	if s.cfg.AuthToken != "" {
		got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AuthToken)) != 1 {
			jsonError(w, "invalid or missing bearer token", http.StatusUnauthorized)
			return false
		}
	}
	return true
}

// ---- handlers ---------------------------------------------------------------

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, consoleConfigResponse{
		WriteEnabled:   s.cfg.WriteEnabled,
		AuthRequired:   s.cfg.AuthToken != "",
		LoadgenEnabled: s.cfg.LoadgenURL != "",
	})
}

func (s *Server) handleNamespaces(w http.ResponseWriter, r *http.Request) {
	var list corev1.NamespaceList
	if err := s.k8sClient.List(r.Context(), &list); err != nil {
		jsonError(w, "could not list namespaces: "+err.Error(), http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns := strings.TrimSpace(r.URL.Query().Get("namespace"))

	var opts []client.ListOption
	if ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	var deps appsv1.DeploymentList
	if err := s.k8sClient.List(ctx, &deps, opts...); err != nil {
		jsonError(w, "could not list deployments: "+err.Error(), http.StatusInternalServerError)
		return
	}

	managed := s.managedTargets(ctx)
	hpaTargets := s.hpaTargets(ctx)

	out := make([]deploymentInfo, 0, len(deps.Items))
	for i := range deps.Items {
		d := &deps.Items[i]
		var replicas int32
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		} else {
			replicas = d.Status.Replicas
		}
		key := d.Namespace + "/" + d.Name
		out = append(out, deploymentInfo{
			Namespace: d.Namespace,
			Name:      d.Name,
			Replicas:  replicas,
			Managed:   managed[key],
			HasHPA:    hpaTargets[key],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

// handleAutoscalers lists CRs (GET) or creates one (POST).
func (s *Server) handleAutoscalers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listAutoscalers(w, r)
	case http.MethodPost:
		s.createAutoscaler(w, r)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) listAutoscalers(w http.ResponseWriter, r *http.Request) {
	var list scalingv1alpha1.AgenticAutoscalerList
	if err := s.k8sClient.List(r.Context(), &list); err != nil {
		jsonError(w, "could not list autoscalers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]autoscalerSummary, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, summarize(&list.Items[i]))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createAutoscaler(w http.ResponseWriter, r *http.Request) {
	if !s.requireWrite(w, r) {
		return
	}
	var req OnboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.Normalize()
	if err := req.Validate(); err != nil {
		jsonError(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if code, err := s.validateAgainstCluster(r.Context(), &req); err != nil {
		jsonError(w, err.Error(), code)
		return
	}

	cr := req.ToCR()
	if err := s.k8sClient.Create(r.Context(), cr); err != nil {
		if apierrors.IsAlreadyExists(err) {
			jsonError(w, fmt.Sprintf("autoscaler %s/%s already exists", cr.Namespace, cr.Name), http.StatusConflict)
			return
		}
		jsonError(w, "could not create autoscaler: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, summarize(cr))
}

// handleAutoscalerItem serves GET/PATCH/DELETE on /api/autoscalers/{ns}/{name}.
func (s *Server) handleAutoscalerItem(w http.ResponseWriter, r *http.Request) {
	ns, name, ok := splitItemPath(r.URL.Path)
	if !ok {
		jsonError(w, "path must be /api/autoscalers/{namespace}/{name}", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getAutoscaler(w, r, ns, name)
	case http.MethodPatch:
		s.patchAutoscaler(w, r, ns, name)
	case http.MethodDelete:
		s.deleteAutoscaler(w, r, ns, name)
	default:
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getAutoscaler(w http.ResponseWriter, r *http.Request, ns, name string) {
	var cr scalingv1alpha1.AgenticAutoscaler
	if err := s.k8sClient.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			jsonError(w, "autoscaler not found", http.StatusNotFound)
			return
		}
		jsonError(w, "could not read autoscaler: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, cr)
}

func (s *Server) patchAutoscaler(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !s.requireWrite(w, r) {
		return
	}
	var body patchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	var cr scalingv1alpha1.AgenticAutoscaler
	if err := s.k8sClient.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			jsonError(w, "autoscaler not found", http.StatusNotFound)
			return
		}
		jsonError(w, "could not read autoscaler: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if body.DryRun != nil {
		cr.Spec.DryRun = *body.DryRun
	}
	if body.MinReplicas != nil {
		cr.Spec.MinReplicas = *body.MinReplicas
	}
	if body.MaxReplicas != nil {
		cr.Spec.MaxReplicas = *body.MaxReplicas
	}
	if cr.Spec.MaxReplicas < 1 || cr.Spec.MinReplicas < 0 || cr.Spec.MinReplicas > cr.Spec.MaxReplicas {
		jsonError(w, "invalid bounds: require 0 <= minReplicas <= maxReplicas and maxReplicas >= 1", http.StatusUnprocessableEntity)
		return
	}

	if err := s.k8sClient.Update(r.Context(), &cr); err != nil {
		jsonError(w, "could not update autoscaler: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summarize(&cr))
}

func (s *Server) deleteAutoscaler(w http.ResponseWriter, r *http.Request, ns, name string) {
	if !s.requireWrite(w, r) {
		return
	}
	cr := &scalingv1alpha1.AgenticAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
	}
	if err := s.k8sClient.Delete(r.Context(), cr); err != nil {
		if apierrors.IsNotFound(err) {
			jsonError(w, "autoscaler not found", http.StatusNotFound)
			return
		}
		jsonError(w, "could not delete autoscaler: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- load generator proxy ---------------------------------------------------

func (s *Server) handleLoadStart(w http.ResponseWriter, r *http.Request) { s.proxyLoad(w, r, "/start") }
func (s *Server) handleLoadStop(w http.ResponseWriter, r *http.Request)  { s.proxyLoad(w, r, "/stop") }
func (s *Server) handleLoadStatus(w http.ResponseWriter, r *http.Request) {
	s.proxyLoad(w, r, "/status")
}

// proxyLoad forwards a request to the load generator control API. It exists so
// the browser only ever talks to the operator, never directly to the generator.
func (s *Server) proxyLoad(w http.ResponseWriter, r *http.Request, path string) {
	if !s.requireWrite(w, r) {
		return
	}
	if s.cfg.LoadgenURL == "" {
		jsonError(w, "load generator not configured", http.StatusServiceUnavailable)
		return
	}

	target := strings.TrimRight(s.cfg.LoadgenURL, "/") + path
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	outReq, err := http.NewRequestWithContext(ctx, r.Method, target, r.Body)
	if err != nil {
		jsonError(w, "could not build load request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	outReq.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(outReq)
	if err != nil {
		jsonError(w, "load generator unreachable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// ---- helpers ----------------------------------------------------------------

// managedTargets returns the set of "namespace/deployment" keys already managed
// by an AgenticAutoscaler. Errors are swallowed so the deployment list still
// renders (every entry simply shows unmanaged).
func (s *Server) managedTargets(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	var list scalingv1alpha1.AgenticAutoscalerList
	if err := s.k8sClient.List(ctx, &list); err != nil {
		return set
	}
	for i := range list.Items {
		aa := &list.Items[i]
		ns := aa.Spec.Namespace
		if ns == "" {
			ns = aa.Namespace
		}
		set[ns+"/"+aa.Spec.TargetDeployment] = true
	}
	return set
}

// hpaTargets returns the set of "namespace/deployment" keys that already have an
// HPA targeting them.
func (s *Server) hpaTargets(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	var list autoscalingv2.HorizontalPodAutoscalerList
	if err := s.k8sClient.List(ctx, &list); err != nil {
		return set
	}
	for i := range list.Items {
		h := &list.Items[i]
		if h.Spec.ScaleTargetRef.Kind == "Deployment" {
			set[h.Namespace+"/"+h.Spec.ScaleTargetRef.Name] = true
		}
	}
	return set
}

// validateAgainstCluster runs the checks that need a live client: the target
// Deployment must exist, and in calibrated mode the proposed bounds must nest
// inside the referenced HPA's bounds (mirrors the pkg/policy enforcer rule).
func (s *Server) validateAgainstCluster(ctx context.Context, req *OnboardRequest) (int, error) {
	depNS := req.Spec.Namespace
	if depNS == "" {
		depNS = req.Namespace
	}
	var dep appsv1.Deployment
	if err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: depNS, Name: req.Spec.TargetDeployment}, &dep); err != nil {
		if apierrors.IsNotFound(err) {
			return http.StatusUnprocessableEntity, fmt.Errorf("deployment %s/%s not found", depNS, req.Spec.TargetDeployment)
		}
		return http.StatusInternalServerError, fmt.Errorf("look up deployment: %w", err)
	}

	if req.Spec.HPACoexistence.Mode != "calibrated" {
		return 0, nil
	}
	hpaNS := req.Spec.HPACoexistence.HPANamespace
	if hpaNS == "" {
		hpaNS = depNS
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := s.k8sClient.Get(ctx, types.NamespacedName{Namespace: hpaNS, Name: req.Spec.HPACoexistence.HPAName}, &hpa); err != nil {
		if apierrors.IsNotFound(err) {
			return http.StatusUnprocessableEntity, fmt.Errorf("hpa %s/%s not found", hpaNS, req.Spec.HPACoexistence.HPAName)
		}
		return http.StatusInternalServerError, fmt.Errorf("look up hpa: %w", err)
	}
	var hpaMin int32 = 1
	if hpa.Spec.MinReplicas != nil {
		hpaMin = *hpa.Spec.MinReplicas
	}
	if req.Spec.MinReplicas < hpaMin {
		return http.StatusUnprocessableEntity, fmt.Errorf("minReplicas %d is below hpa minReplicas %d", req.Spec.MinReplicas, hpaMin)
	}
	if req.Spec.MaxReplicas > hpa.Spec.MaxReplicas {
		return http.StatusUnprocessableEntity, fmt.Errorf("maxReplicas %d exceeds hpa maxReplicas %d", req.Spec.MaxReplicas, hpa.Spec.MaxReplicas)
	}
	return 0, nil
}

func summarize(aa *scalingv1alpha1.AgenticAutoscaler) autoscalerSummary {
	targetNS := aa.Spec.Namespace
	if targetNS == "" {
		targetNS = aa.Namespace
	}
	return autoscalerSummary{
		Namespace:          aa.Namespace,
		Name:               aa.Name,
		TargetDeployment:   aa.Spec.TargetDeployment,
		TargetNamespace:    targetNS,
		MinReplicas:        aa.Spec.MinReplicas,
		MaxReplicas:        aa.Spec.MaxReplicas,
		CurrentReplicas:    aa.Status.CurrentReplicas,
		DryRun:             aa.Spec.DryRun,
		Mode:               aa.Spec.HPACoexistence.Mode,
		Provider:           aa.Spec.AIProvider.Provider,
		LastDecisionReason: aa.Status.LastDecisionReason,
		LastScaleTime:      aa.Status.LastScaleTime,
		CoexistenceStatus:  aa.Status.HPACoexistenceStatus,
	}
}

// splitItemPath extracts {namespace} and {name} from /api/autoscalers/{ns}/{name}.
func splitItemPath(path string) (ns, name string, ok bool) {
	rest := strings.TrimPrefix(path, "/api/autoscalers/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
