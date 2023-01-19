# cloudmapresolver
A gRPC name resolver inspired by [sercand/kuberesolver](https://github.com/sercand/kuberesolver) but using the CloudMap ServiceDiscovery API ([DiscoverInstances API](https://docs.aws.amazon.com/cloud-map/latest/api/API_DiscoverInstances.html)) instead of Kubernetes Endpoints.

## Usage
```
import (
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/jon-whit/cloudmapresolver"
)

cfg, err := config.LoadDefaultConfig(
    context.Background(),
    config.WithDefaultRegion("us-west-2"),
)
if err != nil {
    log.Fatalf("failed to initialize AWS SDK config, %v", err)
}

// register the cloudmapresolver
resolver.Register(cloudmapresolver.NewBuilder(cfg))

// with 'cloudmap' schema, grpc will use cloudmapresolver to resolve addresses
cc, err := grpc.Dial("cloudmap:///service.namespace:portname", opts...)
```

## Client-side Load Balancing
You can use `cloudmapresolver` with a `round_robin` load balancing policy to provide client-side load balancing to each CloudMap service endpoint. For example,

```
conn, err := grpc.DialContext(
    context.Background(),
    "cloudmap:///myservice.mynamespace.demo",
    grpc.WithBalancerName("round_robin"),
    grpc.WithInsecure(),
)
if err != {
    ...
}

_ = conn // conn will be balanced between the CloudMap service endpoints.
```

Take a look at [gRPC Load Balancing](https://grpc.io/blog/grpc-load-balancing/) for more information.