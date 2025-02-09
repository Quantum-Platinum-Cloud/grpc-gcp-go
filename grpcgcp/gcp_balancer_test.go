package grpcgcp

import (
	"context"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/grpc-gcp-go/grpcgcp/mocks"
	"github.com/golang/mock/gomock"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/resolver"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/testing/protocmp"

	pb "github.com/GoogleCloudPlatform/grpc-gcp-go/grpcgcp/grpc_gcp"
)

var testApiConfig = &pb.ApiConfig{
	ChannelPool: &pb.ChannelPoolConfig{
		MinSize:                          uint32(5),
		MaxSize:                          uint32(10),
		MaxConcurrentStreamsLowWatermark: uint32(50),
		FallbackToReady:                  true,
	},
	Method: []*pb.MethodConfig{
		{
			Name: []string{"method1", "method2"},
			Affinity: &pb.AffinityConfig{
				Command:     pb.AffinityConfig_BOUND,
				AffinityKey: "boundKey",
			},
		},
		{
			Name: []string{"method3"},
			Affinity: &pb.AffinityConfig{
				Command:     pb.AffinityConfig_BIND,
				AffinityKey: "bindKey",
			},
		},
	},
}

func TestDefaultConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCC := mocks.NewMockClientConn(mockCtrl)
	mockCC.EXPECT().NewSubConn(gomock.Any(), gomock.Any()).DoAndReturn(func(_, _ interface{}) (*mocks.MockSubConn, error) {
		sc := mocks.NewMockSubConn(mockCtrl)
		sc.EXPECT().Connect().AnyTimes()
		sc.EXPECT().UpdateAddresses(gomock.Any()).AnyTimes()
		return sc, nil
	}).AnyTimes()

	wantCfg := &pb.ApiConfig{
		ChannelPool: &pb.ChannelPoolConfig{
			MinSize:                          defaultMinSize,
			MaxSize:                          defaultMaxSize,
			MaxConcurrentStreamsLowWatermark: defaultMaxStreams,
			FallbackToReady:                  false,
		},
		Method: []*pb.MethodConfig{},
	}

	b := newBuilder().Build(mockCC, balancer.BuildOptions{}).(*gcpBalancer)
	// Simulate ClientConn calls UpdateClientConnState with no config.
	b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{},
	})

	if diff := cmp.Diff(wantCfg, b.cfg.ApiConfig, protocmp.Transform()); diff != "" {
		t.Errorf("gcp_balancer config has unexpected difference (-want +got):\n%v", diff)
	}

	b = newBuilder().Build(mockCC, balancer.BuildOptions{}).(*gcpBalancer)
	// Simulate ClientConn calls UpdateClientConnState with empty config.
	b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState:  resolver.State{},
		BalancerConfig: &GcpBalancerConfig{},
	})

	if diff := cmp.Diff(wantCfg, b.cfg.ApiConfig, protocmp.Transform()); diff != "" {
		t.Errorf("gcp_balancer config has unexpected difference (-want +got):\n%v", diff)
	}
}

func TestConfig(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCC := mocks.NewMockClientConn(mockCtrl)
	mockCC.EXPECT().NewSubConn(gomock.Any(), gomock.Any()).DoAndReturn(func(_, _ interface{}) (*mocks.MockSubConn, error) {
		sc := mocks.NewMockSubConn(mockCtrl)
		sc.EXPECT().Connect().AnyTimes()
		sc.EXPECT().UpdateAddresses(gomock.Any()).AnyTimes()
		return sc, nil
	}).AnyTimes()

	b := newBuilder().Build(mockCC, balancer.BuildOptions{}).(*gcpBalancer)
	// Simulate ClientConn calls UpdateClientConnState with the config provided to Dial.
	b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{},
		BalancerConfig: &GcpBalancerConfig{
			ApiConfig: testApiConfig,
		},
	})

	if diff := cmp.Diff(testApiConfig, b.cfg.ApiConfig, protocmp.Transform()); diff != "" {
		t.Errorf("gcp_balancer config has unexpected difference (-want +got):\n%v", diff)
	}
}

func TestParseConfig(t *testing.T) {
	json, err := protojson.Marshal(testApiConfig)
	if err != nil {
		t.Fatalf("cannot encode ApiConfig: %v", err)
	}
	cfg, err := newBuilder().(balancer.ConfigParser).ParseConfig(json)
	if err != nil {
		t.Fatalf("ParseConfig returns error: %v, want: nil", err)
	}
	if diff := cmp.Diff(testApiConfig, cfg, protocmp.Transform()); diff != "" {
		t.Errorf("ParseConfig() result has unexpected difference (-want +got):\n%v", diff)
	}
}

func TestCreatesMinSubConns(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	mockCC := mocks.NewMockClientConn(mockCtrl)
	newSCs := []*mocks.MockSubConn{}
	mockCC.EXPECT().NewSubConn(gomock.Any(), gomock.Any()).DoAndReturn(func(_, _ interface{}) (*mocks.MockSubConn, error) {
		newSC := mocks.NewMockSubConn(mockCtrl)
		newSC.EXPECT().Connect().Times(2)
		newSC.EXPECT().UpdateAddresses(gomock.Any()).AnyTimes()
		newSCs = append(newSCs, newSC)
		return newSC, nil
	}).Times(3)

	b := newBuilder().Build(mockCC, balancer.BuildOptions{}).(*gcpBalancer)
	// Simulate ClientConn calls UpdateClientConnState with the config provided to Dial.
	b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{},
		BalancerConfig: &GcpBalancerConfig{
			ApiConfig: &pb.ApiConfig{
				ChannelPool: &pb.ChannelPoolConfig{
					MinSize:                          3,
					MaxSize:                          10,
					MaxConcurrentStreamsLowWatermark: 100,
				},
			},
		},
	})

	if want := 3; len(b.scRefs) != want {
		t.Fatalf("gcpBalancer scRefs length is %v, want %v", len(b.scRefs), want)
	}
	for _, v := range newSCs {
		if _, ok := b.scRefs[v]; !ok {
			t.Fatalf("Created SubConn is not stored in gcpBalancer.scRefs")
		}
	}
}

func TestRefreshesSubConnsWhenUnresponsive(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	// A slice to store all SubConns created by gcpBalancer's ClientConn.
	newSCs := []*mocks.MockSubConn{}
	mockCC := mocks.NewMockClientConn(mockCtrl)
	mockCC.EXPECT().UpdateState(gomock.Any()).AnyTimes()
	mockCC.EXPECT().RemoveSubConn(gomock.Any()).Times(2)
	mockCC.EXPECT().NewSubConn(gomock.Any(), gomock.Any()).DoAndReturn(func(_, _ interface{}) (*mocks.MockSubConn, error) {
		newSC := mocks.NewMockSubConn(mockCtrl)
		newSC.EXPECT().Connect().MinTimes(1)
		newSC.EXPECT().UpdateAddresses(gomock.Any()).AnyTimes()
		newSCs = append(newSCs, newSC)
		return newSC, nil
	}).Times(6)

	b := newBuilder().Build(mockCC, balancer.BuildOptions{}).(*gcpBalancer)
	// Simulate ClientConn calls UpdateClientConnState with the config provided to Dial.
	b.UpdateClientConnState(balancer.ClientConnState{
		ResolverState: resolver.State{},
		BalancerConfig: &GcpBalancerConfig{
			ApiConfig: &pb.ApiConfig{
				ChannelPool: &pb.ChannelPoolConfig{
					MinSize:                          3,
					MaxSize:                          10,
					MaxConcurrentStreamsLowWatermark: 50,
					UnresponsiveDetectionMs:          100,
					UnresponsiveCalls:                3,
				},
			},
		},
	})

	// Make subConn 0 ready.
	b.UpdateSubConnState(newSCs[0], balancer.SubConnState{ConnectivityState: connectivity.Ready})

	call := func(expSC balancer.SubConn, errOnDone error) {
		ctx := context.TODO()
		var cancel context.CancelFunc
		if errOnDone == deErr {
			ctx, cancel = context.WithTimeout(ctx, 0)
			defer cancel()
		}
		pr, err := b.picker.Pick(balancer.PickInfo{FullMethodName: "", Ctx: ctx})
		if pr.SubConn != expSC || err != nil {
			t.Fatalf("gcpPicker.Pick returns %v, %v, want: %v, nil", pr.SubConn, err, expSC)
		}
		pr.Done(balancer.DoneInfo{Err: errOnDone})
	}

	// First deadline exceeded call.
	call(newSCs[0], deErr)

	time.Sleep(time.Millisecond * 50)

	// Successful call.
	call(newSCs[0], nil)

	time.Sleep(time.Millisecond * 60)

	// Deadline exceeded calls.
	call(newSCs[0], deErr)
	call(newSCs[0], deErr)

	// Should not trigger new subconn as only ~60ms passed since last response.
	call(newSCs[0], deErr)
	if got, want := len(newSCs), 3; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}

	time.Sleep(time.Millisecond * 50)
	// ~110ms since last response and >= 3 deadline exceeded calls. Should trigger new subconn.
	call(newSCs[0], deErr)
	if got, want := len(newSCs), 4; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}

	// Until the fresh SubConn is ready, the old SubConn should be used.
	dlCtx, cancel := context.WithTimeout(context.TODO(), 0)
	defer cancel()
	pr, err := b.picker.Pick(balancer.PickInfo{FullMethodName: "", Ctx: dlCtx})
	if want := newSCs[0]; pr.SubConn != want || err != nil {
		t.Fatalf("gcpPicker.Pick returns %v, %v, want: %v, nil", pr.SubConn, err, want)
	}
	doneOnOld := pr.Done

	// Make replacement subConn 3 ready.
	b.UpdateSubConnState(newSCs[3], balancer.SubConnState{ConnectivityState: connectivity.Ready})

	// Fresh subConn should be picked.
	pr, err = b.picker.Pick(balancer.PickInfo{FullMethodName: "", Ctx: context.Background()})
	if want := newSCs[3]; pr.SubConn != want || err != nil {
		t.Fatalf("gcpPicker.Pick returns %v, %v, want: %v, nil", pr.SubConn, err, want)
	}

	// A call on the old subconn is finished. Should not panic.
	doneOnOld(balancer.DoneInfo{Err: deErr})

	time.Sleep(time.Millisecond * 110)
	call(newSCs[3], deErr)
	call(newSCs[3], deErr)
	// Should not trigger new subconn as unresponsiveTimeMs should've been doubled when still no response on the fresh subconn.
	call(newSCs[3], deErr)
	if got, want := len(newSCs), 4; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}

	time.Sleep(time.Millisecond * 110)
	// After doubled unresponsiveTimeMs has passed, should trigger new subconn.
	call(newSCs[3], deErr)
	if got, want := len(newSCs), 5; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}

	// Make replacement subConn 4 ready.
	b.UpdateSubConnState(newSCs[4], balancer.SubConnState{ConnectivityState: connectivity.Ready})

	// Successful call to reset refresh counter.
	call(newSCs[4], nil)

	time.Sleep(time.Millisecond * 110)
	call(newSCs[4], deErr)
	// Only second deadline exceeded call since last response, should not trigger new subconn.
	call(newSCs[4], deErr)
	if got, want := len(newSCs), 5; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}
	// Third call should trigger new subconn.
	call(newSCs[4], deErr)
	if got, want := len(newSCs), 6; got != want {
		t.Fatalf("Unexpected number of subConns: %d, want %d", got, want)
	}
}
