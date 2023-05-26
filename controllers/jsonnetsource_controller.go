/*
Copyright 2023.

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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/fluxcd/pkg/http/fetch"
	"github.com/fluxcd/pkg/tar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	sourcev1b2 "github.com/fluxcd/source-controller/api/v1beta2"
	"github.com/ghodss/yaml"
	extensionv1alpha1 "github.com/gianlucam76/jsonnet-controller/api/v1alpha1"
	"github.com/go-logr/logr"
	"github.com/google/go-jsonnet"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	libsveltosv1alpha1 "github.com/projectsveltos/libsveltos/api/v1alpha1"
	logs "github.com/projectsveltos/libsveltos/lib/logsettings"
	libsveltosset "github.com/projectsveltos/libsveltos/lib/set"
)

// JsonnetSourceReconciler reconciles a JsonnetSource object
type JsonnetSourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	ConcurrentReconciles int
	PolicyMux            sync.Mutex                                    // use a Mutex to update Map as MaxConcurrentReconciles is higher than one
	ReferenceMap         map[corev1.ObjectReference]*libsveltosset.Set // key: Referenced object; value: set of all JsonnetSources referencing the resource
	JsonnetSourceMap     map[types.NamespacedName]*libsveltosset.Set   // key: JsonnetSource namespace/name; value: set of referenced resources
}

//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=jsonnetsources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=jsonnetsources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=extension.projectsveltos.io,resources=jsonnetsources/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=gitrepositories,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=gitrepositories/status,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=ocirepositories,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=ocirepositories/status,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=buckets,verbs=get;watch;list
//+kubebuilder:rbac:groups="source.toolkit.fluxcd.io",resources=buckets/status,verbs=get;watch;list

func (r *JsonnetSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.V(logs.LogInfo).Info("Reconciling")

	// Fecth the JsonnetSource instance
	jsonnetSource := &extensionv1alpha1.JsonnetSource{}
	if err := r.Get(ctx, req.NamespacedName, jsonnetSource); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		logger.Error(err, "Failed to fetch JsonnetSource")
		return reconcile.Result{}, errors.Wrapf(
			err,
			"Failed to fetch JsonnetSource %s",
			req.NamespacedName,
		)
	}

	logger = logger.WithValues("jsonnetSource", req.String())

	helper, err := patch.NewHelper(jsonnetSource, r.Client)
	if err != nil {
		return reconcile.Result{}, errors.Wrap(err, "failed to init patch helper")
	}

	// Always close the scope when exiting this function so we can persist any ClusterSummary
	// changes.
	defer func() {
		err = r.Close(ctx, jsonnetSource, helper)
		if err != nil {
			reterr = err
		}
	}()

	// Handle deleted JsonnetSource
	if !jsonnetSource.DeletionTimestamp.IsZero() {
		r.cleanMaps(jsonnetSource)
		return reconcile.Result{}, nil
	}

	// Handle non-deleted clusterSummary
	var resources string
	resources, err = r.reconcileNormal(ctx, jsonnetSource, logger)
	if err != nil {
		msg := err.Error()
		jsonnetSource.Status.FailureMessage = &msg
		jsonnetSource.Status.Resources = ""
	} else {
		jsonnetSource.Status.FailureMessage = nil
		jsonnetSource.Status.Resources = resources
	}

	return reconcile.Result{}, err
}

func (r *JsonnetSourceReconciler) reconcileNormal(
	ctx context.Context,
	jsonnetSource *extensionv1alpha1.JsonnetSource,
	logger logr.Logger,
) (string, error) {

	logger.V(logs.LogInfo).Info("Reconciling JsonnetSource")

	r.updateMaps(jsonnetSource, logger)

	tmpDir, err := r.prepareFileSystem(ctx, jsonnetSource, logger)
	if err != nil {
		return "", err
	}

	if tmpDir == "" {
		return "", nil
	}

	defer os.RemoveAll(tmpDir)

	// check build path exists
	filePath := filepath.Join(tmpDir, jsonnetSource.Spec.Path)
	_, err = os.Stat(filePath)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("jsonnet path not found: %v", err))
		return "", err
	}

	vm := jsonnet.MakeVM()

	var subdirectories []string
	subdirectories, err = getAllSubdirectories(filepath.Dir(filePath))
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Failed to get subdirectories in %s: %v\n",
			tmpDir, err))
		return "", err
	}

	for _, subdir := range subdirectories {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("including subdirectory: %s", subdir))
	}

	vm.Importer(&jsonnet.FileImporter{
		JPaths: subdirectories,
	})

	for key, value := range jsonnetSource.Spec.Variables {
		logger.V(logs.LogDebug).Info(fmt.Sprintf("setting variable: %s:%s", key, value))
		vm.ExtVar(key, value)
	}

	jsonnetContent, err := os.ReadFile(filePath)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("Error reading JSONnet file: %v\n", err))
		return "", err
	}

	jsonData, err := vm.EvaluateAnonymousSnippet(filePath, string(jsonnetContent))
	if err != nil {
		return "", err
	}

	result, err := processData(jsonData)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to process YAML: %v\n", err))
		return "", err
	}

	logger.V(logs.LogInfo).Info("Reconciling JsonnetSource success")
	return result, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *JsonnetSourceReconciler) SetupWithManager(mgr ctrl.Manager,
) (controller.Controller, error) {

	c, err := ctrl.NewControllerManagedBy(mgr).
		For(&extensionv1alpha1.JsonnetSource{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5,
		}).
		Build(r)
	if err != nil {
		return nil, errors.Wrap(err, "error creating controller")
	}

	// When ConfigMap changes, according to ConfigMapPredicates,
	// one or more ClusterSummaries need to be reconciled.
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueJsonnetSourceForReference),
		ConfigMapPredicates(mgr.GetLogger().WithValues("predicate", "configmappredicate")),
	)
	if err != nil {
		return nil, err
	}

	// When Secret changes, according to SecretPredicates,
	// one or more ClusterSummaries need to be reconciled.
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueJsonnetSourceForReference),
		SecretPredicates(mgr.GetLogger().WithValues("predicate", "secretpredicate")),
	)

	return c, err
}

func (r *JsonnetSourceReconciler) WatchForFlux(mgr ctrl.Manager, c controller.Controller) error {
	// When a Flux source (GitRepository/OCIRepository/Bucket) changes, one or more ClusterSummaries
	// need to be reconciled.

	err := c.Watch(&source.Kind{Type: &sourcev1.GitRepository{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueJsonnetSourceForFluxSources),
		FluxSourcePredicates(r.Scheme, mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &sourcev1b2.OCIRepository{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueJsonnetSourceForFluxSources),
		FluxSourcePredicates(r.Scheme, mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
	if err != nil {
		return err
	}

	return c.Watch(&source.Kind{Type: &sourcev1b2.Bucket{}},
		handler.EnqueueRequestsFromMapFunc(r.requeueJsonnetSourceForFluxSources),
		FluxSourcePredicates(r.Scheme, mgr.GetLogger().WithValues("predicate", "fluxsourcepredicate")),
	)
}

func (r *JsonnetSourceReconciler) getReferenceMapForEntry(entry *corev1.ObjectReference) *libsveltosset.Set {
	s := r.ReferenceMap[*entry]
	if s == nil {
		s = &libsveltosset.Set{}
		r.ReferenceMap[*entry] = s
	}
	return s
}

func (r *JsonnetSourceReconciler) getCurrentReference(jsonnetSource *extensionv1alpha1.JsonnetSource) *corev1.ObjectReference {
	return &corev1.ObjectReference{
		APIVersion: getReferenceAPIVersion(jsonnetSource),
		Kind:       jsonnetSource.Spec.Kind,
		Namespace:  jsonnetSource.Spec.Namespace,
		Name:       jsonnetSource.Spec.Name,
	}
}

func (r *JsonnetSourceReconciler) updateMaps(jsonnetSource *extensionv1alpha1.JsonnetSource, logger logr.Logger) {
	logger.V(logs.LogDebug).Info("update policy map")
	ref := r.getCurrentReference(jsonnetSource)

	currentReference := &libsveltosset.Set{}
	currentReference.Insert(ref)

	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	// Get list of References not referenced anymore by JsonnetSource
	var toBeRemoved []corev1.ObjectReference
	JsonnetSourceName := types.NamespacedName{Namespace: jsonnetSource.Namespace, Name: jsonnetSource.Name}
	if v, ok := r.JsonnetSourceMap[JsonnetSourceName]; ok {
		toBeRemoved = v.Difference(currentReference)
	}

	JsonnetSourceInfo := getKeyFromObject(r.Scheme, jsonnetSource)
	// For currently referenced instance, add JsonnetSource as consumer
	r.getReferenceMapForEntry(ref).Insert(JsonnetSourceInfo)

	// For each resource not reference anymore, remove ClusterSummary as consumer
	for i := range toBeRemoved {
		referencedResource := toBeRemoved[i]
		r.getReferenceMapForEntry(&referencedResource).Erase(
			JsonnetSourceInfo,
		)
	}

	// Update list of resources currently referenced by ClusterSummary
	r.JsonnetSourceMap[JsonnetSourceName] = currentReference
}

func (r *JsonnetSourceReconciler) cleanMaps(jsonnetSource *extensionv1alpha1.JsonnetSource) {
	r.PolicyMux.Lock()
	defer r.PolicyMux.Unlock()

	delete(r.JsonnetSourceMap, types.NamespacedName{Namespace: jsonnetSource.Namespace, Name: jsonnetSource.Name})

	jsonnetSourceInfo := getKeyFromObject(r.Scheme, jsonnetSource)

	for i := range r.ReferenceMap {
		JsonnetSourceSet := r.ReferenceMap[i]
		JsonnetSourceSet.Erase(jsonnetSourceInfo)
	}
}

func (r *JsonnetSourceReconciler) prepareFileSystem(ctx context.Context,
	jsonnetSource *extensionv1alpha1.JsonnetSource, logger logr.Logger) (string, error) {

	ref := r.getCurrentReference(jsonnetSource)

	if ref.Kind == string(libsveltosv1alpha1.ConfigMapReferencedResourceKind) {
		return prepareFileSystemWithConfigMap(ctx, r.Client, ref, logger)
	} else if ref.Kind == string(libsveltosv1alpha1.SecretReferencedResourceKind) {
		return prepareFileSystemWithSecret(ctx, r.Client, ref, logger)
	}

	return prepareFileSystemWithFluxSource(ctx, r.Client, ref, logger)
}

func prepareFileSystemWithConfigMap(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, logger logr.Logger) (string, error) {

	configMap, err := getConfigMap(ctx, c, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(configMap.BinaryData, ref, logger)
}

func prepareFileSystemWithSecret(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, logger logr.Logger) (string, error) {

	secret, err := getSecret(ctx, c, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name})
	if err != nil {
		return "", err
	}

	return prepareFileSystemWithData(secret.Data, ref, logger)
}

func prepareFileSystemWithData(binaryData map[string][]byte,
	ref *corev1.ObjectReference, logger logr.Logger) (string, error) {

	key := "jsonnet.tar.gz"
	binaryTarGz, ok := binaryData[key]
	if !ok {
		return "", fmt.Errorf("%s missing", key)
	}

	// Create tmp dir.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("jsonnet-%s-%s",
		ref.Namespace, ref.Name))
	if err != nil {
		err = fmt.Errorf("tmp dir error: %w", err)
		return "", err
	}

	filePath := path.Join(tmpDir, key)

	err = os.WriteFile(filePath, binaryTarGz, permission0600)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to write file %s: %v", filePath, err))
		return "", err
	}

	tmpDir = path.Join(tmpDir, "extracted")

	err = extractTarGz(filePath, tmpDir)
	if err != nil {
		logger.V(logs.LogInfo).Info(fmt.Sprintf("failed to extract tar.gz: %v", err))
		return "", err
	}

	logger.V(logs.LogDebug).Info("extracted .tar.gz")
	return tmpDir, nil
}

func prepareFileSystemWithFluxSource(ctx context.Context, c client.Client,
	ref *corev1.ObjectReference, logger logr.Logger) (string, error) {

	fluxSource, err := getSource(ctx, c, ref)
	if err != nil {
		return "", err
	}

	if fluxSource == nil {
		return "", fmt.Errorf("source %s %s/%s not found",
			ref.Kind, ref.Namespace, ref.Name)
	}

	if fluxSource.GetArtifact() == nil {
		msg := "Source is not ready, artifact not found"
		logger.V(logs.LogInfo).Info(msg)
		return "", err
	}

	// Create tmp dir.
	tmpDir, err := os.MkdirTemp("", fmt.Sprintf("kustomization-%s-%s",
		ref.Namespace, ref.Name))
	if err != nil {
		err = fmt.Errorf("tmp dir error: %w", err)
		return "", err
	}

	artifactFetcher := fetch.NewArchiveFetcher(
		1,
		tar.UnlimitedUntarSize,
		tar.UnlimitedUntarSize,
		os.Getenv("SOURCE_CONTROLLER_LOCALHOST"),
	)

	// Download artifact and extract files to the tmp dir.
	err = artifactFetcher.Fetch(fluxSource.GetArtifact().URL, fluxSource.GetArtifact().Digest, tmpDir)
	if err != nil {
		return "", err
	}

	return tmpDir, nil
}

func getSource(ctx context.Context, c client.Client, ref *corev1.ObjectReference) (sourcev1.Source, error) {
	var src sourcev1.Source
	namespacedName := types.NamespacedName{
		Namespace: ref.Namespace,
		Name:      ref.Name,
	}

	switch ref.Kind {
	case sourcev1.GitRepositoryKind:
		var repository sourcev1.GitRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &repository
	case sourcev1b2.OCIRepositoryKind:
		var repository sourcev1b2.OCIRepository
		err := c.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return src, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &repository
	case sourcev1b2.BucketKind:
		var bucket sourcev1b2.Bucket
		err := c.Get(ctx, namespacedName, &bucket)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return src, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		src = &bucket
	default:
		return src, fmt.Errorf("source `%s` kind '%s' not supported",
			ref.Name, ref.Kind)
	}
	return src, nil
}

// Close closes the current scope persisting the JsonnetSource status.
func (s *JsonnetSourceReconciler) Close(ctx context.Context, jsonnetSource *extensionv1alpha1.JsonnetSource,
	patchHelper *patch.Helper) error {

	return patchHelper.Patch(
		ctx,
		jsonnetSource,
	)
}

func processData(jsonData string) (string, error) {
	// try single resource
	u := unstructured.Unstructured{}
	if err := json.Unmarshal([]byte(jsonData), &u); err == nil {
		jsonData, err := u.MarshalJSON()
		return string(jsonData), err
	}

	// try for multiple resources

	// Unmarshal JSON as an array of map[string]interface{}
	var jsonDataArray []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &jsonDataArray); err != nil {
		return "", err
	}

	// Convert each JSON resource to YAML
	var yamlDataArray [][]byte
	for _, jsonData := range jsonDataArray {
		yamlData, err := yaml.Marshal(jsonData)
		if err != nil {
			return "", err
		}
		yamlDataArray = append(yamlDataArray, yamlData)
	}

	// Convert each YAML resource to Unstructured
	var resources []unstructured.Unstructured
	for _, yamlData := range yamlDataArray {
		resource := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(yamlData, resource); err != nil {
			return "", err
		}
		resources = append(resources, *resource)
	}

	result := ""
	for _, resource := range resources {
		jsonData, err := resource.MarshalJSON()
		if err != nil {
			return "", err
		}
		result += "---\n"
		result += string(jsonData)
	}

	return result, nil
}

func getAllSubdirectories(directory string) ([]string, error) {
	var subdirectories []string
	subdirectories = append(subdirectories, directory)

	err := filepath.Walk(directory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() && path != directory {
			subdirectories = append(subdirectories, path)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return subdirectories, nil
}
