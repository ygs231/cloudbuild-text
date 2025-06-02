package common

import (
	"encoding/base64"
	"fmt"
	aaipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/action"
	common "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/common"
	atipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/target"
	"net/url"
	"strings"

	"github.com/ninja-cloudbuild/cloudbuild/server/remote_cache/digest"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/timeutil"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	ampipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/common"
	afpipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/file"
	bespb "github.com/ninja-cloudbuild/cloudbuild/proto/build_event_stream"
	inpb "github.com/ninja-cloudbuild/cloudbuild/proto/invocation"
)

// N.B. This file contains common functions used to format and extract API
// responses from BuildEvents. Changing this code may change the external API,
// so please take care!

// A prefix specifying which ID encoding scheme we're using.
// Don't change this unless you're changing the ID scheme, in which case you should probably check for this
// prefix and support this old ID scheme for some period of time during the migration.
const encodedIDPrefix = "id::v1::"

func EncodeID(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(encodedIDPrefix + id))
}

func TargetLabelKey(groupID, iid, targetLabel string) string {
	return groupID + "/api/t/" + base64.RawURLEncoding.EncodeToString([]byte(iid+targetLabel))
}

// ActionsKey eturns a string key under which target level actions can be
// recorded in a metrics collector.
func ActionLabelKey(groupID, iid, targetLabel string) string {
	return groupID + "/api/a/" + base64.RawURLEncoding.EncodeToString([]byte(iid+targetLabel))
}

func filesFromOutput(output []*bespb.File) []*afpipb.File {
	files := []*afpipb.File{}
	for _, output := range output {
		uri := ""
		switch file := output.File.(type) {
		case *bespb.File_Uri:
			uri = file.Uri
			// Contents files are not currently supported - only the file name will be appended without a uri.
		}
		f := &afpipb.File{
			Name: output.Name,
			Uri:  uri,
		}
		if u, err := url.Parse(uri); err == nil {
			if r, err := digest.ParseDownloadResourceName(u.Path); err == nil {
				f.Hash = r.GetDigest().GetHash()
				f.SizeBytes = r.GetDigest().GetSizeBytes()
			}
		}
		files = append(files, f)
	}
	return files
}

func FillActionFromBuildEvent(event *bespb.BuildEvent, action *aaipb.Action) *aaipb.Action {
	switch event.Payload.(type) {
	case *bespb.BuildEvent_Completed:
		{
			action.TargetLabel = event.GetId().GetTargetCompleted().GetLabel()
			action.Id.TargetId = event.GetId().GetTargetCompleted().GetLabel()
			action.Id.ConfigurationId = event.GetId().GetTargetCompleted().GetConfiguration().Id
			action.Id.ActionId = EncodeID("build")
			return action
		}
	case *bespb.BuildEvent_TestResult:
		{
			testResultID := event.GetId().GetTestResult()
			action.TargetLabel = event.GetId().GetTestResult().GetLabel()
			action.Id.TargetId = event.GetId().GetTestResult().GetLabel()
			action.Id.ConfigurationId = event.GetId().GetTestResult().GetConfiguration().Id
			action.Id.ActionId = EncodeID(fmt.Sprintf("test-S_%d-R_%d-A_%d", testResultID.Shard, testResultID.Run, testResultID.Attempt))
			return action
		}
	}
	return nil
}

func FillActionOutputFilesFromBuildEvent(event *bespb.BuildEvent, action *aaipb.Action) *aaipb.Action {
	switch p := event.Payload.(type) {
	case *bespb.BuildEvent_Completed:
		{
			action.File = filesFromOutput(p.Completed.DirectoryOutput)
			return action
		}
	case *bespb.BuildEvent_TestResult:
		{
			action.File = filesFromOutput(p.TestResult.TestActionOutput)
			return action
		}
	}
	return nil
}

func testStatusToStatus(testStatus bespb.TestStatus) ampipb.Status {
	switch testStatus {
	case bespb.TestStatus_PASSED:
		return ampipb.Status_PASSED
	case bespb.TestStatus_FLAKY:
		return ampipb.Status_FLAKY
	case bespb.TestStatus_TIMEOUT:
		return ampipb.Status_TIMED_OUT
	case bespb.TestStatus_FAILED:
		return ampipb.Status_FAILED
	case bespb.TestStatus_INCOMPLETE:
		return ampipb.Status_INCOMPLETE
	case bespb.TestStatus_REMOTE_FAILURE:
		return ampipb.Status_TOOL_FAILED
	case bespb.TestStatus_FAILED_TO_BUILD:
		return ampipb.Status_FAILED_TO_BUILD
	case bespb.TestStatus_TOOL_HALTED_BEFORE_TESTING:
		return ampipb.Status_CANCELLED
	default:
		return ampipb.Status_STATUS_UNSPECIFIED
	}
}

type TargetMap map[string]*atipb.Target

func (tm TargetMap) ProcessEvent(iid string, event *bespb.BuildEvent) {
	switch p := event.Payload.(type) {
	case *bespb.BuildEvent_Configured:
		{
			ruleType := strings.Replace(p.Configured.TargetKind, " rule", "", -1)
			language := ""
			if components := strings.Split(p.Configured.TargetKind, "_"); len(components) > 1 {
				language = components[0]
			}
			label := event.GetId().GetTargetConfigured().GetLabel()
			tm[label] = &atipb.Target{
				Id: &atipb.Target_Id{
					InvocationId: iid,
					TargetId:     label,
				},
				Label:    label,
				Status:   common.Status(ampipb.Status_BUILDING),
				RuleType: ruleType,
				Language: language,
				Tag:      p.Configured.Tag,
			}
		}
	case *bespb.BuildEvent_Completed:
		{
			target := tm[event.GetId().GetTargetCompleted().GetLabel()]
			target.Status = common.Status(ampipb.Status_BUILT)
		}
	case *bespb.BuildEvent_TestSummary:
		{
			target := tm[event.GetId().GetTestSummary().GetLabel()]
			target.Status = common.Status(testStatusToStatus(p.TestSummary.OverallStatus))
			startTime := timeutil.GetTimeWithFallback(p.TestSummary.FirstStartTime, p.TestSummary.FirstStartTimeMillis)
			duration := timeutil.GetDurationWithFallback(p.TestSummary.TotalRunDuration, p.TestSummary.TotalRunDurationMillis)
			target.Timing = &ampipb.Timing{
				StartTime: timestamppb.New(startTime),
				Duration:  durationpb.New(duration),
			}
		}
	}
}

func TargetMapFromInvocation(inv *inpb.Invocation) TargetMap {
	targetMap := make(TargetMap)
	for _, event := range inv.GetEvent() {
		targetMap.ProcessEvent(inv.GetInvocationId(), event.GetBuildEvent())
	}
	return targetMap
}
