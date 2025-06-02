package api

import (
	"context"
	"flag"
	"net/http"
	"net/url"
	"strings"

	"github.com/ninja-cloudbuild/cloudbuild/proto/resource"
	"github.com/ninja-cloudbuild/cloudbuild/server/build_event_protocol/build_event_handler"
	"github.com/ninja-cloudbuild/cloudbuild/server/bytestream"
	"github.com/ninja-cloudbuild/cloudbuild/server/environment"
	"github.com/ninja-cloudbuild/cloudbuild/server/eventlog"
	"github.com/ninja-cloudbuild/cloudbuild/server/http/protolet"
	"github.com/ninja-cloudbuild/cloudbuild/server/interfaces"
	"github.com/ninja-cloudbuild/cloudbuild/server/remote_cache/digest"
	"github.com/ninja-cloudbuild/cloudbuild/server/tables"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/capabilities"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/log"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/perms"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/prefix"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/query_builder"
	"github.com/ninja-cloudbuild/cloudbuild/server/util/status"
	"google.golang.org/protobuf/proto"

	api_common "github.com/ninja-cloudbuild/cloudbuild/server/api/common"

	aaipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/action"
	afipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/file"
	aiipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/invocation"
	alipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/log"
	apipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/service"
	atipb "github.com/ninja-cloudbuild/cloudbuild/proto/api/v1/target"
	akpb "github.com/ninja-cloudbuild/cloudbuild/proto/api_key"
	bespb "github.com/ninja-cloudbuild/cloudbuild/proto/build_event_stream"
	elpb "github.com/ninja-cloudbuild/cloudbuild/proto/eventlog"
)

var (
	enableAPI            = flag.Bool("api.enable_api", true, "Whether or not to enable the BuildBuddy API.")
	enableCache          = flag.Bool("api.enable_cache", false, "Whether or not to enable the API cache.")
	enableCacheDeleteAPI = flag.Bool("enable_cache_delete_api", false, "If true, enable access to cache delete API.")
)

type APIServer struct {
	env environment.Env
}

func Register(env environment.Env) error {
	if *enableAPI {
		env.SetAPIService(NewAPIServer(env))
	}
	return nil
}

func NewAPIServer(env environment.Env) *APIServer {
	return &APIServer{
		env: env,
	}
}

func (s *APIServer) checkPreconditions(ctx context.Context) (interfaces.UserInfo, error) {
	authenticator := s.env.GetAuthenticator()
	if authenticator == nil {
		return nil, status.FailedPreconditionErrorf("No authenticator configured")
	}
	return s.env.GetAuthenticator().AuthenticatedUser(ctx)
}

func (s *APIServer) authorizeWrites(ctx context.Context) error {
	canWrite, err := capabilities.IsGranted(ctx, s.env, akpb.ApiKey_CACHE_WRITE_CAPABILITY)
	if err != nil {
		return err
	}
	if !canWrite {
		return status.PermissionDeniedError("You do not have permission to perform this file.")
	}
	return nil
}

func (s *APIServer) GetInvocation(ctx context.Context, req *aiipb.GetInvocationRequest) (*aiipb.GetInvocationResponse, error) {
	user, err := s.checkPreconditions(ctx)
	if err != nil {
		return nil, err
	}

	if req.GetSelector().GetInvocationId() == "" && req.GetSelector().GetCommitSha() == "" {
		return nil, status.InvalidArgumentErrorf("InvocationSelector must contain a valid invocation_id or commit_sha")
	}

	q := query_builder.NewQuery(`SELECT * FROM Invocations`)
	q = q.AddWhereClause(`group_id = ?`, user.GetGroupID())
	if req.GetSelector().GetInvocationId() != "" {
		q = q.AddWhereClause(`invocation_id = ?`, req.GetSelector().GetInvocationId())
	}
	if req.GetSelector().GetCommitSha() != "" {
		q = q.AddWhereClause(`commit_sha = ?`, req.GetSelector().GetCommitSha())
	}
	if err := perms.AddPermissionsCheckToQuery(ctx, s.env, q); err != nil {
		return nil, err
	}
	queryStr, args := q.Build()

	rows, err := s.env.GetDBHandle().DB(ctx).Raw(queryStr, args...).Rows()
	if err != nil {
		return nil, err
	}

	invocations := []*aiipb.Invocation{}
	for rows.Next() {
		var ti tables.Invocation
		if err := s.env.GetDBHandle().DB(ctx).ScanRows(rows, &ti); err != nil {
			return nil, err
		}

		apiInvocation := &aiipb.Invocation{
			Id: &aiipb.Invocation_Id{
				InvocationId: ti.InvocationID,
			},
			Success:       ti.Success,
			User:          ti.User,
			DurationUsec:  ti.DurationUsec,
			Host:          ti.Host,
			Command:       ti.Command,
			Pattern:       ti.Pattern,
			ActionCount:   ti.ActionCount,
			CreatedAtUsec: ti.CreatedAtUsec,
			UpdatedAtUsec: ti.UpdatedAtUsec,
			RepoUrl:       ti.RepoURL,
			BranchName:    ti.BranchName,
			CommitSha:     ti.CommitSHA,
			Role:          ti.Role,
		}

		invocations = append(invocations, apiInvocation)
	}

	if req.IncludeMetadata {
		for _, i := range invocations {
			inv, err := build_event_handler.LookupInvocation(s.env, ctx, i.Id.InvocationId)
			if err != nil {
				return nil, err
			}
			for _, event := range inv.GetEvent() {
				switch p := event.BuildEvent.Payload.(type) {
				case *bespb.BuildEvent_BuildMetadata:
					{
						for k, v := range p.BuildMetadata.Metadata {
							i.BuildMetadata = append(i.BuildMetadata, &aiipb.InvocationMetadata{
								Key:   k,
								Value: v,
							})
						}
					}
				case *bespb.BuildEvent_WorkspaceStatus:
					{
						for _, item := range p.WorkspaceStatus.Item {
							i.WorkspaceStatus = append(i.WorkspaceStatus, &aiipb.InvocationMetadata{
								Key:   item.Key,
								Value: item.Value,
							})
						}
					}
				}
			}
		}
	}

	return &aiipb.GetInvocationResponse{
		Invocation: invocations,
	}, nil
}

func (s *APIServer) CacheEnabled() bool {
	return *enableCache
}

func (s *APIServer) redisCachedTarget(ctx context.Context, userInfo interfaces.UserInfo, iid, targetLabel string) (*atipb.Target, error) {
	if !s.CacheEnabled() || s.env.GetMetricsCollector() == nil {
		return nil, nil
	}

	if targetLabel == "" {
		return nil, nil
	}
	key := api_common.TargetLabelKey(userInfo.GetGroupID(), iid, targetLabel)
	blobs, err := s.env.GetMetricsCollector().GetAll(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(blobs) != 1 {
		return nil, nil
	}

	t := &atipb.Target{}
	if err := proto.Unmarshal([]byte(blobs[0]), t); err != nil {
		return nil, err
	}
	return t, nil
}

func (s *APIServer) GetTarget(ctx context.Context, req *atipb.GetTargetRequest) (*atipb.GetTargetResponse, error) {
	userInfo, err := s.checkPreconditions(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetSelector().GetInvocationId() == "" {
		return nil, status.InvalidArgumentErrorf("TargetSelector must contain a valid invocation_id")
	}
	iid := req.GetSelector().GetInvocationId()

	rsp := &atipb.GetTargetResponse{
		Target: make([]*atipb.Target, 0),
	}

	cacheKey := req.GetSelector().GetLabel()
	// Target ID is equal to the target label, so either can be used as a cache key.
	if targetId := req.GetSelector().GetTargetId(); targetId != "" {
		cacheKey = targetId
	}

	cachedTarget, err := s.redisCachedTarget(ctx, userInfo, iid, cacheKey)
	if err != nil {
		log.Debugf("redisCachedTarget err: %s", err)
	} else if cachedTarget != nil {
		if targetMatchesTargetSelector(cachedTarget, req.GetSelector()) {
			rsp.Target = append(rsp.Target, cachedTarget)
		}
	}
	if len(rsp.Target) > 0 {
		return rsp, nil
	}

	inv, err := build_event_handler.LookupInvocation(s.env, ctx, req.GetSelector().GetInvocationId())
	if err != nil {
		return nil, err
	}
	targetMap := api_common.TargetMapFromInvocation(inv)

	// Filter to only selected targets.
	targets := []*atipb.Target{}
	for _, target := range targetMap {
		if targetMatchesTargetSelector(target, req.GetSelector()) {
			targets = append(targets, target)
		}
	}

	return &atipb.GetTargetResponse{
		Target: targets,
	}, nil
}

func (s *APIServer) redisCachedActions(ctx context.Context, userInfo interfaces.UserInfo, iid, targetLabel string) ([]*aaipb.Action, error) {
	if !s.CacheEnabled() || s.env.GetMetricsCollector() == nil {
		return nil, nil
	}

	if targetLabel == "" {
		return nil, nil
	}

	const limit = 100_000
	key := api_common.ActionLabelKey(userInfo.GetGroupID(), iid, targetLabel)
	serializedResults, err := s.env.GetMetricsCollector().ListRange(ctx, key, 0, limit-1)
	if err != nil {
		return nil, err
	}
	a := &aaipb.Action{}
	actions := make([]*aaipb.Action, 0)
	for _, serializedResult := range serializedResults {
		if err := proto.Unmarshal([]byte(serializedResult), a); err != nil {
			return nil, err
		}
		actions = append(actions, a)
	}
	return actions, nil
}

func (s *APIServer) GetAction(ctx context.Context, req *aaipb.GetActionRequest) (*aaipb.GetActionResponse, error) {
	userInfo, err := s.checkPreconditions(ctx)
	if err != nil {
		return nil, err
	}

	if req.GetSelector().GetInvocationId() == "" {
		return nil, status.InvalidArgumentErrorf("ActionSelector must contain a valid invocation_id")
	}
	iid := req.GetSelector().GetInvocationId()
	rsp := &aaipb.GetActionResponse{
		Action: make([]*aaipb.Action, 0),
	}

	cacheKey := req.GetSelector().GetTargetLabel()
	// Target ID is equal to the target label, so either can be used as a cache key.
	if targetId := req.GetSelector().GetTargetId(); targetId != "" {
		cacheKey = targetId
	}

	cachedActions, err := s.redisCachedActions(ctx, userInfo, iid, cacheKey)
	if err != nil {
		log.Debugf("redisCachedAction err: %s", err)
	}
	for _, action := range cachedActions {
		if action != nil && actionMatchesActionSelector(action, req.GetSelector()) {
			rsp.Action = append(rsp.Action, action)
		}
	}
	if len(rsp.Action) > 0 {
		return rsp, nil
	}

	inv, err := build_event_handler.LookupInvocation(s.env, ctx, iid)
	if err != nil {
		return nil, err
	}

	for _, event := range inv.GetEvent() {
		action := &aaipb.Action{
			Id: &aaipb.Action_Id{
				InvocationId: inv.InvocationId,
			},
		}
		action = api_common.FillActionFromBuildEvent(event.BuildEvent, action)

		// Filter to only selected actions.
		if action != nil && actionMatchesActionSelector(action, req.GetSelector()) {
			action = api_common.FillActionOutputFilesFromBuildEvent(event.BuildEvent, action)
			rsp.Action = append(rsp.Action, action)
		}
	}

	return rsp, nil
}

func (s *APIServer) GetLog(ctx context.Context, req *alipb.GetLogRequest) (*alipb.GetLogResponse, error) {
	// No need for user here because user filters will be applied by LookupInvocation.
	if _, err := s.checkPreconditions(ctx); err != nil {
		return nil, err
	}

	if req.GetSelector().GetInvocationId() == "" {
		return nil, status.InvalidArgumentErrorf("LogSelector must contain a valid invocation_id")
	}

	chunkReq := &elpb.GetEventLogChunkRequest{
		InvocationId: req.GetSelector().GetInvocationId(),
		ChunkId:      req.GetPageToken(),
	}

	resp, err := eventlog.GetEventLogChunk(ctx, s.env, chunkReq)
	if err != nil {
		log.Errorf("Encountered error getting event log chunk: %s\nRequest: %s", err, chunkReq)
		return nil, err
	}

	return &alipb.GetLogResponse{
		Log: &alipb.Log{
			Contents: string(resp.GetBuffer()),
		},
		NextPageToken: resp.GetNextChunkId(),
	}, nil
}

type getFileWriter struct {
	s apipb.ApiService_GetFileServer
}

func (gfs *getFileWriter) Write(data []byte) (int, error) {
	err := gfs.s.Send(&afipb.GetFileResponse{
		Data: data,
	})
	return len(data), err
}

func (s *APIServer) GetFile(req *afipb.GetFileRequest, server apipb.ApiService_GetFileServer) error {
	ctx := server.Context()
	if _, err := s.checkPreconditions(ctx); err != nil {
		return err
	}

	parsedURL, err := url.Parse(req.GetUri())
	if err != nil {
		return status.InvalidArgumentErrorf("Invalid URL")
	}

	writer := &getFileWriter{s: server}

	return bytestream.StreamBytestreamFile(ctx, s.env, parsedURL, writer)
}

func (s *APIServer) DeleteFile(ctx context.Context, req *afipb.DeleteFileRequest) (*afipb.DeleteFileResponse, error) {
	if !*enableCacheDeleteAPI {
		return nil, status.PermissionDeniedError("DeleteFile API not enabled")
	}

	ctx, err := prefix.AttachUserPrefixToContext(ctx, s.env)
	if err != nil {
		return nil, err
	}

	if _, err = s.checkPreconditions(ctx); err != nil {
		return nil, err
	}
	if err = s.authorizeWrites(ctx); err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(req.GetUri())
	if err != nil {
		return nil, status.InvalidArgumentErrorf("Invalid URL")
	}
	urlStr := strings.TrimPrefix(parsedURL.RequestURI(), "/")

	var resourceName *resource.ResourceName
	if digest.IsActionCacheResourceName(urlStr) {
		parsedRN, err := digest.ParseActionCacheResourceName(urlStr)
		if err != nil {
			return nil, status.InvalidArgumentErrorf("Invalid URL. Does not match expected actioncache URI pattern: %s", err)
		}
		resourceName = digest.NewACResourceName(parsedRN.GetDigest(), parsedRN.GetInstanceName()).ToProto()
	} else if digest.IsDownloadResourceName(urlStr) {
		parsedRN, err := digest.ParseDownloadResourceName(urlStr)
		if err != nil {
			return nil, status.InvalidArgumentErrorf("Invalid URL. Does not match expected CAS URI pattern: %s", err)
		}
		resourceName = digest.NewCASResourceName(parsedRN.GetDigest(), parsedRN.GetInstanceName()).ToProto()
	} else {
		return nil, status.InvalidArgumentErrorf("Invalid URL. Only actioncache and CAS URIs supported.")
	}

	err = s.env.GetCache().Delete(ctx, resourceName)
	if err != nil && !status.IsNotFoundError(err) {
		return nil, err
	}

	return &afipb.DeleteFileResponse{}, nil
}

// Handle streaming http GetFile request since protolet doesn't handle streaming rpcs yet.
func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, err := s.checkPreconditions(r.Context()); err != nil {
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return
	}

	req := afipb.GetFileRequest{}
	protolet.ReadRequestToProto(r, &req)

	parsedURL, err := url.Parse(req.GetUri())
	if err != nil {
		http.Error(w, "Invalid URI", http.StatusBadRequest)
		return
	}

	err = bytestream.StreamBytestreamFile(r.Context(), s.env, parsedURL, w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Returns true if a selector has an empty target ID or matches the target's ID or tag
func targetMatchesTargetSelector(target *atipb.Target, selector *atipb.TargetSelector) bool {
	if selector.Label != "" {
		return selector.Label == target.Label
	}

	if selector.Tag != "" {
		for _, tag := range target.GetTag() {
			if tag == selector.Tag {
				return true
			}
		}
		return false
	}
	return selector.TargetId == "" || selector.TargetId == target.GetId().TargetId
}

// Returns true if a selector doesn't specify a particular id or matches the target's ID
func actionMatchesActionSelector(action *aaipb.Action, selector *aaipb.ActionSelector) bool {
	return (selector.TargetId == "" || selector.TargetId == action.GetId().TargetId) &&
		(selector.TargetLabel == "" || selector.TargetLabel == action.GetTargetLabel()) &&
		(selector.ConfigurationId == "" || selector.ConfigurationId == action.GetId().ConfigurationId) &&
		(selector.ActionId == "" || selector.ActionId == action.GetId().ActionId)
}
