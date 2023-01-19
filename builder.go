package cloudmapresolver

import (
	"context"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/resolver"

	"github.com/aws/aws-sdk-go-v2/aws"
	sd "github.com/aws/aws-sdk-go-v2/service/servicediscovery"
)

const (
	cloudmapSchema = "cloudmap"
	defaultFreq    = 1 * time.Minute
)

func NewBuilder(config aws.Config) resolver.Builder {
	return &cloudmapResolver{
		config: config,
	}
}

// cloudmapResolver implements both a grpc resolver.Builder and resolver.Resolver
type cloudmapResolver struct {
	config    aws.Config
	service   string
	namespace string
	sdclient  *sd.Client
	ctx       context.Context
	cancel    context.CancelFunc
	cc        resolver.ClientConn

	t    *time.Timer
	freq time.Duration

	// rn channel is used by ResolveNow() to force an immediate resolution of the target.
	rn chan struct{}

	// wg is used to enfoce Close() to return after the watcher() goroutine has finished.
	wg sync.WaitGroup
}

// ResolveNow triggers the actual name resolution.
func (r *cloudmapResolver) ResolveNow(options resolver.ResolveNowOptions) {
	select {
	case r.rn <- struct{}{}:
	default:
	}
}

func (r *cloudmapResolver) Close() {
	r.cancel()
	r.wg.Wait()
}

// Build returns a new instance of a resolver.Resolver
//
// target is split on the first "." into a service name with the remainder as the namespace.
// Example: my.name.local will resolve to service "my" and namespace "name.local"
func (r *cloudmapResolver) Build(
	target resolver.Target,
	cc resolver.ClientConn,
	opts resolver.BuildOptions,
) (resolver.Resolver, error) {
	if target.URL.Scheme != cloudmapSchema {
		return nil, fmt.Errorf("unsupported scheme in CloudMap service discovery. Must be '%s'", cloudmapSchema)
	}
	comps := strings.SplitN(target.URL.Host, ".", 2)

	ctx, cancel := context.WithCancel(context.Background())

	cmResolver := &cloudmapResolver{
		ctx:       ctx,
		cancel:    cancel,
		sdclient:  sd.NewFromConfig(r.config),
		cc:        cc,
		t:         time.NewTimer(defaultFreq),
		freq:      defaultFreq,
		service:   comps[0],
		namespace: comps[1],
	}

	go until(func() {
		r.wg.Add(1)
		err := cmResolver.watch()
		if err != nil && err != io.EOF {
			grpclog.Errorf("cloudmapresolver: watching ended with error='%v', will reconnect again", err)
		}
	}, time.Second, ctx.Done())

	return cmResolver, nil
}

func (r *cloudmapResolver) Scheme() string {
	return cloudmapSchema
}

func (r *cloudmapResolver) resolve() {
	res, err := r.sdclient.DiscoverInstances(context.Background(), &sd.DiscoverInstancesInput{
		NamespaceName: aws.String(r.namespace),
		ServiceName:   aws.String(r.service),
	})
	if err != nil {
		log.Fatalf("CloudMap DiscoverInstances failed with error: %v\n", err)
	}

	// extract metadata of registered instances, i.e. the availability zone
	// for Fargate tasks with service discovery ECS will automatically create those
	// attributes
	//
	// the availability zone is then put into BalancerAttributes of the
	// resolver.Address struct, so the balancer.PickerBuilder can access
	// and use them
	addr := make([]resolver.Address, 0, len(res.Instances))
	for i := range res.Instances {
		instAtt := res.Instances[i].Attributes

		ip := instAtt["AWS_INSTANCE_IPV4"]
		port := instAtt["AWS_INSTANCE_PORT"]

		add := fmt.Sprintf("%s:%s", ip, port)

		log.Printf("resolved target: %v", add)

		balancerAtts := attributes.New("az", instAtt["AVAILABILITY_ZONE"])
		addr = append(addr, resolver.Address{
			Addr:               add,
			BalancerAttributes: balancerAtts,
		})
	}

	r.cc.UpdateState(resolver.State{
		Addresses: addr,
	})

	// Next lookup should happen after an interval defined by r.freq.
	r.t.Reset(r.freq)
}

func (r *cloudmapResolver) watch() error {
	defer r.wg.Done()

	r.resolve()

	for {
		select {
		case <-r.ctx.Done():
			return nil
		case <-r.t.C:
			r.resolve()
		case <-r.rn:
			r.resolve()
		}
	}
}

func until(f func(), period time.Duration, stopCh <-chan struct{}) {
	select {
	case <-stopCh:
		return
	default:
	}
	for {
		func() {
			defer handleCrash()
			f()
		}()
		select {
		case <-stopCh:
			return
		case <-time.After(period):
		}
	}
}

// HandleCrash simply catches a crash and logs an error. Meant to be called via defer.
func handleCrash() {
	if r := recover(); r != nil {
		callers := string(debug.Stack())
		grpclog.Errorf("cloudmapresolver: recovered from panic: %#v (%v)\n%v", r, r, callers)
	}
}
