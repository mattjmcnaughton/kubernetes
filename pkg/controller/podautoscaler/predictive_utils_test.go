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

// @TODO Should this be `podautoscaler_test`? `horizontal_test.go` uses
// `podautoscaler` as the package, so I am emulating that.
package podautoscaler

import (
	"github.com/stretchr/testify/assert"
	"math"
	"testing"
	"time"

	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/unversioned"
)

func TestUpdateUtilizationObservationsNoPrevious(t *testing.T) {
	// Start with no previous observations and add a single observation.
	cpuUtil := 70
	previousObs := []map[string]int{}
	podInit := 5.0

	currentTime, err := time.Now().MarshalJSON()
	assert.Nil(t, err)

	recordedObs, err := updateUtilizationObservations(cpuUtil, string(currentTime[:]), previousObs, podInit)
	assert.Nil(t, err)

	assert.Equal(t, len(recordedObs), 1, "Should have added one observation.")

	for _, value := range recordedObs[0] {
		assert.Equal(t, value, cpuUtil, "Should record CPU util as value in map.")
	}
}

func TestUpdateUtilizationObservationsOnePrevious(t *testing.T) {
	// Start with one previous observation.
	timeNow, err := time.Now().Add(-5 * time.Second).MarshalJSON()
	assert.Nil(t, err)

	previousObs := []map[string]int{{string(timeNow[:]): 50}}

	cpuUtil := 70
	podInit := 5.0

	currentTime, err := time.Now().MarshalJSON()
	assert.Nil(t, err)

	recordedObs, err := updateUtilizationObservations(cpuUtil, string(currentTime[:]), previousObs, podInit)
	assert.Nil(t, err)

	assert.Equal(t, len(recordedObs), 2, "Should have a total of two observations.")
}

func TestUpdateUtilizationsObservationsRemoveAllPrevious(t *testing.T) {
	// Start with one previous observation from 11 minutes ago that will be
	// removed (because the cutoff is 10 minutes).
	oldTime, err := time.Now().Add(-11 * time.Minute).MarshalJSON()
	assert.Nil(t, err)

	previousObs := []map[string]int{{string(oldTime[:]): 50}}

	cpuUtil := 70
	podInit := 5.0

	currentTime, err := time.Now().MarshalJSON()
	assert.Nil(t, err)

	recordedObs, err := updateUtilizationObservations(cpuUtil, string(currentTime[:]), previousObs, podInit)
	assert.Nil(t, err)

	assert.Equal(t, len(recordedObs), 1, "Only one observation should remain.")
}

func TestUpdateUtilizationsObservationsRemoveSomePrevious(t *testing.T) {
	// Start with one previous observation from 11 minutes ago that will be
	// removed (because the cutoff is 10 minutes).
	oldTime, err := time.Now().Add(-11 * time.Minute).MarshalJSON()
	assert.Nil(t, err)

	lessOldTime, err := time.Now().Add(-1 * time.Minute).MarshalJSON()
	assert.Nil(t, err)

	previousObs := []map[string]int{
		{string(oldTime[:]): 50},
		{string(lessOldTime[:]): 10},
	}

	cpuUtil := 70
	podInit := 5.0

	currentTime, err := time.Now().MarshalJSON()
	assert.Nil(t, err)

	recordedObs, err := updateUtilizationObservations(cpuUtil, string(currentTime[:]), previousObs, podInit)
	assert.Nil(t, err)

	assert.Equal(t, len(recordedObs), 2, "Only one observation should be removed.")
}

// TestUpdateUtilizationsObservationsRemoveReplicas tests that we do not
// write any observations that are direct replicas of what we previously
// recorded.
func TestUpdateUtilizationsObservationsRemoveReplicas(t *testing.T) {
	oldTime, err := time.Now().Add(-1 * time.Minute).MarshalJSON()
	assert.Nil(t, err)

	replicaTime := oldTime

	cpuUtil := 70
	podInit := 5.0

	previousObs := []map[string]int{
		{string(oldTime[:]): 50},
	}

	recordedObs, err := updateUtilizationObservations(cpuUtil, string(replicaTime[:]), previousObs, podInit)
	assert.Nil(t, err)

	assert.Equal(t, len(recordedObs), 1, "Should not make a duplicate observation.")

}

func TestInitTimeForPods(t *testing.T) {
	testPods := createTestPods()

	initTime, err := initTimeForPods(testPods)

	assert.Nil(t, err)
	assert.True(t, initTime > 0.0, "Act. init time should be desired init time.")
}

func TestGetSecondsAndCPULists(t *testing.T) {
	currentTime, err := time.Now().MarshalJSON()
	assert.Nil(t, err)

	previousUtils := []map[string]int{
		{string(currentTime[:]): 50.0},
		{string(currentTime[:]): 60.0},
	}

	xVals, yVals := getSecondsAndCPULists(previousUtils)

	assert.Equal(t, len(xVals), 2, "There should be two time values.")
	assert.Equal(t, len(yVals), 2, "There should be two CPU utilizations")
}

func TestLineOfBestFit(t *testing.T) {
	seconds := []float64{3.0, 5.0, 3.0, 7.0, 5.0, 8.0, 7.0, 4.0, 6.0, 2.0}
	allCPUUtilizations := []float64{21.0, 26.0, 20.0, 32.0, 23.0, 42.0, 35.0, 24.0, 30.0, 17.0}

	yIntercept, slope, err := lineOfBestFit(seconds, allCPUUtilizations)

	assert.Nil(t, err)
	assert.True(t, math.Abs(3.7-*slope) < 1, "The slope should be 3.7")
	assert.True(t, math.Abs(8.5-*yIntercept) < 1, "The y intercept should be near 8.5")
}

func TestPredictFutureCPUFromBestBit(t *testing.T) {
	pit := 5.0
	currentTime := 0.0
	yIntercept := 0.0
	slope := 1.0

	predictedFuture := predictFutureCPUFromBestFit(pit, currentTime, yIntercept, slope)

	assert.Equal(t, 5.0, predictedFuture, "Predictied Future CPU util should be 5.0")
}

func createTestPods() []api.Pod {
	creationTimestamp := time.Now().Add(-5 * time.Minute)

	time1 := time.Now().Add(-1 * time.Minute)
	time2 := time.Now().Add(-2 * time.Minute)

	podStatus1 := api.PodStatus{
		Conditions: []api.PodCondition{
			{
				Type:               api.PodReady,
				LastTransitionTime: unversioned.NewTime(time1),
			},
		},
	}

	podStatus2 := api.PodStatus{
		Conditions: []api.PodCondition{
			{
				Type:               api.PodReady,
				LastTransitionTime: unversioned.NewTime(time2),
			},
		},
	}

	pod1 := api.Pod{
		Status: podStatus1,
		ObjectMeta: api.ObjectMeta{
			CreationTimestamp: unversioned.NewTime(creationTimestamp),
		},
	}

	pod2 := api.Pod{
		Status: podStatus2,
		ObjectMeta: api.ObjectMeta{
			CreationTimestamp: unversioned.NewTime(creationTimestamp),
		},
	}

	return []api.Pod{pod1, pod2}
}
