/*
Copyright 2015 The Kubernetes Authors All rights reserved.

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

package podautoscaler

import (
	"fmt"
	"math"
	"time"

	"github.com/montanaflynn/stats"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/apis/extensions"
)

const (
	// PitAnnotationName is the name of the key for the
	// pod's initialization time in the obejct's annotations.
	PitAnnotationName = "InitializationTime"

	// PredictiveAutoscalingAnnotationName must have a value of "true" in
	// the annotations hash to enable predictive auto-scaling.
	PredictiveAutoscalingAnnotationName = "predictive"

	// PreviousCPUAnnotationName is the name in which we store a JSON map of
	// previous CPU utilization observations and the time at which they
	// occured.
	PreviousCPUAnnotationName = "previousCPUUtilizations"
)

// isPredictive is a helper function for checking if this auto-scaler is
// functioning in predictive mode. Currently, this variable is set through
// annotation.
func isPredictive(hpa *extensions.HorizontalPodAutoscaler) bool {
	if predAnn, found := hpa.Annotations[PredictiveAutoscalingAnnotationName]; found {
		return predAnn == "true"
	}

	return false
}

// updateUtilizationObservations takes the current utilization, the previous
// observations, and the pod initialization time and adds the current
// observation to the previous observations to return the observations that
// should be recorded in the auto-scaler object.
func updateUtilizationObservations(cpuCurrentUtilization int, previousObservations []map[string]int, podInitTime float64) ([]map[string]int, error) {
	jsonTime, err := time.Now().MarshalJSON()
	if err != nil {
		return nil, err
	}

	observation := map[string]int{string(jsonTime[:]): cpuCurrentUtilization}
	previousObservations = removeOldUtils(previousObservations, podInitTime)

	updatedObservations := append(previousObservations, observation)
	return updatedObservations, nil
}

// removeOldUtils removes any previous CPU observations that we no longer wish
// to record.
func removeOldUtils(previousObservations []map[string]int, podInitTime float64) []map[string]int {
	k := 20.0
	// Don't predict on more than 10 minutes - given a 30 second sync
	// period, this is a maximum of 20 stored observations.
	maxDistance := math.Min(podInitTime*k, 60.0*10.0)
	firstToKeep := -1
	stop := false
	var t time.Time

	for i, timeMap := range previousObservations {
		if stop {
			break
		}

		for key := range timeMap {
			t.UnmarshalJSON([]byte(key))

			// We add new observations to the end, so we are looking
			// for the first observation within the range we want,
			// and we are guaranteed that all after it will also be
			// in the range.
			if time.Since(t).Seconds() < maxDistance {
				firstToKeep = i
				stop = true
			}
		}
	}

	if firstToKeep == -1 {
		return []map[string]int{}
	}

	return previousObservations[firstToKeep:]
}

// initTimeForPods returns the average initialization time for all of these
// pods.
func initTimeForPods(pods []api.Pod) (float64, error) {
	totalInitializationTime := 0.0
	readyPods := 0

	for _, pod := range pods {
		pit, err := initTimeForPod(pod)
		if err != nil {
			return 0.0, err
		}

		totalInitializationTime += pit
		readyPods++
	}

	if readyPods == 0 {
		return 0.0, nil
	}

	return totalInitializationTime / float64(readyPods), nil
}

// initTimeForPod returns the amount of time it takes a single pod to
// initialize.
func initTimeForPod(pod api.Pod) (float64, error) {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == api.PodReady {
			initTime, found := pod.Annotations[PitAnnotationName]
			if !found {
				initTime = cond.LastTransitionTime.Time.Sub(pod.CreationTimestamp.Time).String()
				writeToPodAnnotations(&pod, PitAnnotationName, initTime)
			}

			initDuration, err := time.ParseDuration(initTime)
			if err != nil {
				return 0.0, err
			}

			return initDuration.Seconds(), nil
		}
	}

	return 0.0, fmt.Errorf("Pod is not ready.")
}

// getSecondsAndCPULists takes what was recorded in `hpa.Annotations` and
// returns two separate lists of seconds and CPU Util lists which can be through
// of as the x and y values respective.
func getSecondsAndCPULists(previousUtils []map[string]int) ([]float64, []float64) {
	allSeconds := []float64{}
	allCPUUtilizations := []float64{}

	for _, obs := range previousUtils {
		for obsTime, cpuUtil := range obs {
			var t time.Time
			t.UnmarshalJSON([]byte(obsTime))

			allSeconds = append(allSeconds, float64(t.Unix()))
			allCPUUtilizations = append(allCPUUtilizations, float64(cpuUtil))
		}
	}

	return allSeconds, allCPUUtilizations
}

// lineOfBestFit is a helper method for calculating the line of best fit for cpu
// utilization. We calculate the slope of this line of best fit
// using COV_{xy} / VAR_{x}, with the understanding that this is the slope of
// the line that will minimize the squared verical deviations from said line.
// http://www.radford.edu/~rsheehy/Gen_flash/Tutorials/Linear_Regression/reg-tut.htm
func lineOfBestFit(allSeconds []float64, allCPUUtilizations []float64) (*float64, *float64, error) {
	covariance, err := stats.CovariancePopulation(allSeconds, allCPUUtilizations)
	if err != nil {
		return nil, nil, err
	}

	secondsVariance, err := stats.PopulationVariance(allSeconds)
	if err != nil {
		return nil, nil, err
	}

	if secondsVariance == 0 {
		return nil, nil, fmt.Errorf("Can't divide by variance if it is 0.")
	}

	meanSeconds, err := stats.Mean(allSeconds)
	if err != nil {
		return nil, nil, err
	}

	meanCPUUtilization, err := stats.Mean(allCPUUtilizations)
	if err != nil {
		return nil, nil, err
	}

	slope := covariance / secondsVariance
	yIntercept := meanCPUUtilization - (slope * meanSeconds)

	return &yIntercept, &slope, nil
}

func predictFutureCPUFromBestFit(pit float64, currentTime float64, yIntercept float64, slope float64) float64 {
	futurePredictionTime := currentTime + pit
	predictedCPUUtilization := yIntercept + (slope * futurePredictionTime)

	return predictedCPUUtilization
}

// writeToHPAAnnotations is a wrapper method for writing to `Annotations` of an
// horizontal pod autoscaler including the check that `Annotations` is not nil
// first.
// @TODO This should be combined with `writeToPodAnnotations` so there is one
// general method that both can call.
func writeToHPAAnnotations(hpa *extensions.HorizontalPodAutoscaler, key string, value string) {
	if hpa.Annotations == nil {
		hpa.Annotations = make(map[string]string)
	}

	hpa.Annotations[key] = value
}

// writeToPodAnnotations is same as `writeToHPAAnnotations`, eventually they
// should be combined.
func writeToPodAnnotations(pod *api.Pod, key string, value string) {
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations[key] = value
}
