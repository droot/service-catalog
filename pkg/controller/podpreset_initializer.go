/*
Copyright 2017 The Kubernetes Authors.

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

package controller

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	clientv1 "k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	// coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/golang/glog"
	// settingsinformers "github.com/kubernetes-incubator/service-catalog/pkg/client/informers_generated/externalversions/settings/v1alpha1"
	settingsapi "github.com/kubernetes-incubator/service-catalog/pkg/apis/settings/v1alpha1"
	settingsv1alpha1 "github.com/kubernetes-incubator/service-catalog/pkg/client/clientset_generated/clientset/typed/settings/v1alpha1"
	// settingsclientset "github.com/kubernetes-incubator/service-catalog/pkg/client/clientset_generated/clientset/typed/settings/v1alpha1"
)

const (
	podPresetInitializerName = "podpreset.initializer.k8s.io"
)

// podPresetInitializer implements PodPreset application logic.
type podPresetInitializer struct {
	kubeClient     kubernetes.Interface
	settingsClient settingsv1alpha1.SettingsV1alpha1Interface

	podInformer cache.Controller

	podLister corelisters.PodLister
	recorder  record.EventRecorder

	podQueue workqueue.RateLimitingInterface
}

func NewPodPresetInitializer(
	kubeClient kubernetes.Interface,
	settingsClient settingsv1alpha1.SettingsV1alpha1Interface,
	recorder record.EventRecorder,
) (*podPresetInitializer, error) {

	initializer := &podPresetInitializer{
		kubeClient:     kubeClient,
		settingsClient: settingsClient,
		recorder:       recorder,
		podQueue:       workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "podpreset"),
	}

	var podIndexer cache.Indexer

	podIndexer, initializer.podInformer = cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(lo metav1.ListOptions) (runtime.Object, error) {
				lo.IncludeUninitialized = true
				return kubeClient.CoreV1().Pods(v1.NamespaceAll).List(lo)
			},
			WatchFunc: func(lo metav1.ListOptions) (watch.Interface, error) {
				lo.IncludeUninitialized = true
				return kubeClient.CoreV1().Pods(v1.NamespaceAll).Watch(lo)
			},
		},
		&clientv1.Pod{},
		// 30*time.Second,
		0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				pod := obj.(*clientv1.Pod)
				glog.Infof("new Pod: %s with meta: %+v received", pod.GetName(), pod.ObjectMeta)
				if needsInitialization(pod) {
					glog.Infof("found an uninitialized pod: %+v", pod.Name)
					if key, err := cache.MetaNamespaceKeyFunc(obj); err == nil {
						initializer.podQueue.Add(key)
					}
				}
			},
			// TODO: figure out how do we handle existing uninitialized pods.
			// 1. Ignore them completely (set the resync interval to 0)
			// 2. if no record of previous initialization attempt exists, then
			// we should try to initialize.
			// 3. If previous initialization attempt failed with terminal error,
			// then we should ignore this pod.
			// UpdateFunc: func(old, new interface{}) {
			// 	pod := new.(*clientv1.Pod)
			// 	if needsInitialization(pod) {
			// 		glog.V(4).Infof("found an existing uninitialized pod: %s", pod.GetName())
			// 		if key, err := cache.MetaNamespaceKeyFunc(new); err == nil {
			// 			initializer.podQueue.Add(key)
			// 		}
			// 	}
			// },
		},
		cache.Indexers{},
	)
	initializer.podLister = corelisters.NewPodLister(podIndexer)

	return initializer, nil
}

func (c *podPresetInitializer) Run(stopCh <-chan struct{}) {
	defer func() {
		c.podQueue.ShutDown()
	}()
	glog.Info("Starting podpreset initializer")
	defer glog.Infof("Shutting down podpreset initializer")

	go c.podInformer.Run(stopCh)

	// Wait for all caches to be synced, before processing items from the queue is started
	if !cache.WaitForCacheSync(stopCh, c.podInformer.HasSynced) {
		glog.Error(fmt.Errorf("Timed out waiting for pod cache to sync"))
		return
	}

	// Launching additional goroutines would parallelize workers consuming from the queue (but we don't really need this)
	go wait.Until(c.runWorker, time.Second, stopCh)

	// wait unitl we are told to stop
	<-stopCh
}

func (c *podPresetInitializer) runWorker() {
	for c.processNext() {
	}
}

func (c *podPresetInitializer) processNext() bool {
	// Wait until there is a new item in the working queue
	key, quit := c.podQueue.Get()
	if quit {
		return false
	}
	// Tell the queue that we are done with processing this key. This unblocks the key for other workers
	// This allows safe parallel processing because two pods with the same key are never processed in
	// parallel.
	defer c.podQueue.Done(key)
	// Invoke the method containing the business logic
	err := c.process(key.(string))
	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, key)
	return true
}

func (c *podPresetInitializer) process(key string) error {
	glog.Infof("got key: %v", key)

	ns, podName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		glog.Errorf("error parsing key: %v :%v", key, err)
		return err
	}

	// TODO: check with controller experts if fetching the Pod is preferrable
	// over the one returned by the informer.
	// pod, err := c.kubeClient.CoreV1().Pods(ns).Get(podName, metav1.ListOptions{IncludeUninitialized: true})
	pod, err := c.podLister.Pods(ns).Get(podName)
	if err != nil {
		glog.Infof("error retrieving pod with name: %s in ns: %v", podName, ns)
		return err
	}

	if !needsInitialization(pod) {
		glog.Infof("pod initialization no longer needed so skipping it")
		return nil
	}

	// TODO: check if we should use informer to list podpresets. Given that
	// podpresets will be very less in number.
	// podPresets, err := c.settingsClient.PodPresets(ns).List(metav1.ListOptions{})
	// if err != nil {
	// 	glog.Infof("error fetching podpresets : %v", err)
	// 	return err
	// }
	// glog.Infof("fetched %d number of podpresets in namespace: %s", len(podPresets.Items), ns)

	// get a copy of corev1.Pod from clientv1.Pod
	// apply the business logic of on corev1.Pod
	// convert back corev1.Pod to clientv1.Pod for updates

	podCopy, err := convertClientv1PodToCorev1Pod(pod)
	if err != nil {
		glog.Infof("error copying pod")
		return err
	}

	err = c.admit(ns, podCopy)
	if err != nil {
		glog.Infof("error applying podpreset on pod: %v", err)
		return err
	}

	markInitializationDone(podCopy)

	finalPod, err := convertCorev1PodToClientv1Pod(podCopy)
	if err != nil {
		glog.Infof("error converting corev1.Pod to clientv1.Pod: %v", err)
		return err
	}

	if _, err = c.kubeClient.CoreV1().Pods(ns).Update(finalPod); err != nil {
		glog.Infof("failed to update pod : %v", err)
		return err
	}

	return nil
}

// markInitializationDone removes the PodPreset initializer from the Pod's
// pending initializer list. And if it is the only initializer in the pending
// list, then resets the Initializers field to nil mark the initialization done.
func markInitializationDone(pod *corev1.Pod) {
	pendingInitializers := pod.GetInitializers().Pending
	if len(pendingInitializers) == 1 {
		pod.ObjectMeta.Initializers = nil
	} else {
		pod.ObjectMeta.Initializers.Pending = pod.ObjectMeta.Initializers.Pending[1:]
	}
}

func convertCorev1PodToClientv1Pod(in *corev1.Pod) (out *clientv1.Pod, err error) {
	b, err := json.Marshal(in)
	if err != nil {
		return
	}
	err = json.Unmarshal(b, &out)
	return
}

func convertClientv1PodToCorev1Pod(in *clientv1.Pod) (out *corev1.Pod, err error) {
	b, err := json.Marshal(in)
	if err != nil {
		return
	}
	err = json.Unmarshal(b, &out)
	return
}

func copyObjToPod(obj interface{}) (*clientv1.Pod, error) {
	podCopy, err := runtime.NewScheme().DeepCopy(obj.(*clientv1.Pod))
	if err != nil {
		return nil, err
	}
	pod := podCopy.(*clientv1.Pod)
	return pod, nil
}

// isPodUninitialized determines if Pod is waiting for PodPreset initialization.
func needsInitialization(pod *clientv1.Pod) bool {
	initializers := pod.ObjectMeta.GetInitializers()
	if initializers != nil && len(initializers.Pending) > 0 &&
		initializers.Pending[0].Name == podPresetInitializerName {
		return true
	}
	glog.V(4).Infof("pod %s with initalizers %+v does not need initialization", pod.GetName(), initializers)
	return false
}

func (c *podPresetInitializer) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.podQueue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.podQueue.NumRequeues(key) < 5 {
		glog.Infof("Error processing pod %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.podQueue.AddRateLimited(key)
		return
	}

	c.podQueue.Forget(key)
	glog.Errorf("Dropping node %q out of the queue: %v", key, err)
}

const (
	annotationPrefix = "podpreset.admission.kubernetes.io"
	pluginName       = "PodPreset"
)

// Admit injects a pod with the specific fields for each pod preset it matches.
func (c *podPresetInitializer) admit(ns string, pod *corev1.Pod) error {
	// Ignore all calls to subresources or resources other than pods.
	// Ignore all operations other than CREATE.
	// if len(a.GetSubresource()) != 0 || a.GetResource().GroupResource() != corev1.Resource("pods") || a.GetOperation() != admission.Create {
	// 	return nil
	// }

	// pod, ok := a.GetObject().(*corev1.Pod)
	// if !ok {
	// 	return errors.NewBadRequest("Resource was marked with kind Pod but was unable to be converted")
	// }
	//
	// if _, isMirrorPod := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirrorPod {
	// 	return nil
	// }

	// Ignore if exclusion annotation is present
	if podAnnotations := pod.GetAnnotations(); podAnnotations != nil {
		glog.V(5).Infof("Looking at pod annotations, found: %v", podAnnotations)
		if podAnnotations[corev1.PodPresetOptOutAnnotationKey] == "true" {
			return nil
		}
	}

	// list, err := c.lister.PodPresets(a.GetNamespace()).List(labels.Everything())
	// if err != nil {
	// 	return fmt.Errorf("listing pod presets failed: %v", err)
	// }
	list, err := c.settingsClient.PodPresets(ns).List(metav1.ListOptions{})
	if err != nil {
		glog.Infof("error fetching podpresets : %v", err)
		return err
	}
	glog.Infof("fetched %d number of podpresets in namespace: %s", len(list.Items), ns)

	matchingPPs, err := filterPodPresets(list, pod)
	if err != nil {
		return fmt.Errorf("filtering pod presets failed: %v", err)
	}

	if len(matchingPPs) == 0 {
		return nil
	}

	presetNames := make([]string, len(matchingPPs))
	for i, pp := range matchingPPs {
		presetNames[i] = pp.GetName()
	}

	// detect merge conflict
	err = safeToApplyPodPresetsOnPod(pod, matchingPPs)
	if err != nil {
		// conflict, ignore the error, but raise an event
		glog.Warningf("conflict occurred while applying podpresets: %s on pod: %v err: %v",
			strings.Join(presetNames, ","), pod.GetGenerateName(), err)
		return nil
	}

	applyPodPresetsOnPod(pod, matchingPPs)

	glog.Infof("applied podpresets: %s successfully on Pod: %+v ", strings.Join(presetNames, ","), pod.GetGenerateName())

	return nil
}

// filterPodPresets returns list of PodPresets which match given Pod.
func filterPodPresets(list *settingsapi.PodPresetList, pod *corev1.Pod) ([]*settingsapi.PodPreset, error) {
	var matchingPPs []*settingsapi.PodPreset

	for _, pp := range list.Items {
		selector, err := metav1.LabelSelectorAsSelector(&pp.Spec.Selector)
		if err != nil {
			return nil, fmt.Errorf("label selector conversion failed: %v for selector: %v", pp.Spec.Selector, err)
		}

		// check if the pod labels match the selector
		if !selector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		glog.V(4).Infof("PodPreset %s matches pod %s labels", pp.GetName(), pod.GetName())
		matchingPPs = append(matchingPPs, &pp)
	}
	return matchingPPs, nil
}

// safeToApplyPodPresetsOnPod determines if there is any conflict in information
// injected by given PodPresets in the Pod.
func safeToApplyPodPresetsOnPod(pod *corev1.Pod, podPresets []*settingsapi.PodPreset) error {
	var errs []error

	// volumes attribute is defined at the Pod level, so determine if volumes
	// injection is causing any conflict.
	if _, err := mergeVolumes(pod.Spec.Volumes, podPresets); err != nil {
		errs = append(errs, err)
	}
	for _, ctr := range pod.Spec.Containers {
		if err := safeToApplyPodPresetsOnContainer(&ctr, podPresets); err != nil {
			errs = append(errs, err)
		}
	}
	return utilerrors.NewAggregate(errs)
}

// safeToApplyPodPresetsOnContainer determines if there is any conflict in
// information injected by given PodPresets in the given container.
func safeToApplyPodPresetsOnContainer(ctr *corev1.Container, podPresets []*settingsapi.PodPreset) error {
	var errs []error
	// check if it is safe to merge env vars and volume mounts from given podpresets and
	// container's existing env vars.
	if _, err := mergeEnv(ctr.Env, podPresets); err != nil {
		errs = append(errs, err)
	}
	if _, err := mergeVolumeMounts(ctr.VolumeMounts, podPresets); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// mergeEnv merges a list of env vars with the env vars injected by given list podPresets.
// It returns an error if it detects any conflict during the merge.
func mergeEnv(envVars []corev1.EnvVar, podPresets []*settingsapi.PodPreset) ([]corev1.EnvVar, error) {
	origEnv := map[string]corev1.EnvVar{}
	for _, v := range envVars {
		origEnv[v.Name] = v
	}

	mergedEnv := make([]corev1.EnvVar, len(envVars))
	copy(mergedEnv, envVars)

	var errs []error

	for _, pp := range podPresets {
		for _, v := range pp.Spec.Env {
			found, ok := origEnv[v.Name]
			if !ok {
				// if we don't already have it append it and continue
				origEnv[v.Name] = v
				mergedEnv = append(mergedEnv, v)
				continue
			}

			// make sure they are identical or throw an error
			if !reflect.DeepEqual(found, v) {
				errs = append(errs, fmt.Errorf("merging env for %s has a conflict on %s: \n%#v\ndoes not match\n%#v\n in container", pp.GetName(), v.Name, v, found))
			}
		}
	}

	err := utilerrors.NewAggregate(errs)
	if err != nil {
		return nil, err
	}

	return mergedEnv, err
}

func mergeEnvFrom(envSources []corev1.EnvFromSource, podPresets []*settingsapi.PodPreset) ([]corev1.EnvFromSource, error) {
	var mergedEnvFrom []corev1.EnvFromSource

	mergedEnvFrom = append(mergedEnvFrom, envSources...)
	for _, pp := range podPresets {
		mergedEnvFrom = append(mergedEnvFrom, pp.Spec.EnvFrom...)
	}

	return mergedEnvFrom, nil
}

// mergeVolumeMounts merges given list of VolumeMounts with the volumeMounts
// injected by given podPresets. It returns an error if it detects any conflict during the merge.
func mergeVolumeMounts(volumeMounts []corev1.VolumeMount, podPresets []*settingsapi.PodPreset) ([]corev1.VolumeMount, error) {

	origVolumeMounts := map[string]corev1.VolumeMount{}
	volumeMountsByPath := map[string]corev1.VolumeMount{}
	for _, v := range volumeMounts {
		origVolumeMounts[v.Name] = v
		volumeMountsByPath[v.MountPath] = v
	}

	mergedVolumeMounts := make([]corev1.VolumeMount, len(volumeMounts))
	copy(mergedVolumeMounts, volumeMounts)

	var errs []error

	for _, pp := range podPresets {
		for _, v := range pp.Spec.VolumeMounts {
			found, ok := origVolumeMounts[v.Name]
			if !ok {
				// if we don't already have it append it and continue
				origVolumeMounts[v.Name] = v
				mergedVolumeMounts = append(mergedVolumeMounts, v)
			} else {
				// make sure they are identical or throw an error
				// shall we throw an error for identical volumeMounts ?
				if !reflect.DeepEqual(found, v) {
					errs = append(errs, fmt.Errorf("merging volume mounts for %s has a conflict on %s: \n%#v\ndoes not match\n%#v\n in container", pp.GetName(), v.Name, v, found))
				}
			}

			found, ok = volumeMountsByPath[v.MountPath]
			if !ok {
				// if we don't already have it append it and continue
				volumeMountsByPath[v.MountPath] = v
			} else {
				// make sure they are identical or throw an error
				if !reflect.DeepEqual(found, v) {
					errs = append(errs, fmt.Errorf("merging volume mounts for %s has a conflict on mount path %s: \n%#v\ndoes not match\n%#v\n in container", pp.GetName(), v.MountPath, v, found))
				}
			}
		}
	}

	err := utilerrors.NewAggregate(errs)
	if err != nil {
		return nil, err
	}

	return mergedVolumeMounts, err
}

// mergeVolumes merges given list of Volumes with the volumes injected by given
// podPresets. It returns an error if it detects any conflict during the merge.
func mergeVolumes(volumes []corev1.Volume, podPresets []*settingsapi.PodPreset) ([]corev1.Volume, error) {
	origVolumes := map[string]corev1.Volume{}
	for _, v := range volumes {
		origVolumes[v.Name] = v
	}

	mergedVolumes := make([]corev1.Volume, len(volumes))
	copy(mergedVolumes, volumes)

	var errs []error

	for _, pp := range podPresets {
		for _, v := range pp.Spec.Volumes {
			found, ok := origVolumes[v.Name]
			if !ok {
				// if we don't already have it append it and continue
				origVolumes[v.Name] = v
				mergedVolumes = append(mergedVolumes, v)
				continue
			}

			// make sure they are identical or throw an error
			if !reflect.DeepEqual(found, v) {
				errs = append(errs, fmt.Errorf("merging volumes for %s has a conflict on %s: \n%#v\ndoes not match\n%#v\n in container", pp.GetName(), v.Name, v, found))
			}
		}
	}

	err := utilerrors.NewAggregate(errs)
	if err != nil {
		return nil, err
	}

	if len(mergedVolumes) == 0 {
		return nil, nil
	}

	return mergedVolumes, err
}

func (c *podPresetInitializer) addEvent(pod *corev1.Pod, pip *settingsapi.PodPreset, message string) {
	// ref, err := ref.GetReference(corev1.Scheme, pod)
	// if err != nil {
	// 	glog.Errorf("pip %s: get reference for pod %s failed: %v", pip.GetName(), pod.GetName(), err)
	// 	return
	// }
	//
	// e := &corev1.Event{
	// 	InvolvedObject: *ref,
	// 	Message:        message,
	// 	Source: corev1.EventSource{
	// 		Component: fmt.Sprintf("pip %s", pip.GetName()),
	// 	},
	// 	Type: "Warning",
	// }
	//
	// if _, err := c.client.Core().Events(pod.GetNamespace()).Create(e); err != nil {
	// 	glog.Errorf("pip %s: creating pod event failed: %v", pip.GetName(), err)
	// 	return
	// }
}

// applyPodPresetsOnPod updates the PodSpec with merged information from all the
// applicable PodPresets. It ignores the errors of merge functions because merge
// errors have already been checked in safeToApplyPodPresetsOnPod function.
func applyPodPresetsOnPod(pod *corev1.Pod, podPresets []*settingsapi.PodPreset) {
	if len(podPresets) == 0 {
		return
	}

	volumes, _ := mergeVolumes(pod.Spec.Volumes, podPresets)
	pod.Spec.Volumes = volumes

	for i, ctr := range pod.Spec.Containers {
		applyPodPresetsOnContainer(&ctr, podPresets)
		pod.Spec.Containers[i] = ctr
	}

	// add annotation
	if pod.ObjectMeta.Annotations == nil {
		pod.ObjectMeta.Annotations = map[string]string{}
	}

	for _, pp := range podPresets {
		pod.ObjectMeta.Annotations[fmt.Sprintf("%s/podpreset-%s", annotationPrefix, pp.GetName())] = pp.GetResourceVersion()
	}
}

// applyPodPresetsOnContainer injects envVars, VolumeMounts and envFrom from
// given podPresets in to the given container. It ignores conflict errors
// because it assumes those have been checked already by the caller.
func applyPodPresetsOnContainer(ctr *corev1.Container, podPresets []*settingsapi.PodPreset) {
	envVars, _ := mergeEnv(ctr.Env, podPresets)
	ctr.Env = envVars

	volumeMounts, _ := mergeVolumeMounts(ctr.VolumeMounts, podPresets)
	ctr.VolumeMounts = volumeMounts

	envFrom, _ := mergeEnvFrom(ctr.EnvFrom, podPresets)
	ctr.EnvFrom = envFrom
}
