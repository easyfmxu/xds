package main

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	_ "github.com/envoyproxy/go-control-plane/envoy/api/v2" // for v2.ClusterLoadAssignment
	clusterpb "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointpb "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	cdspb "github.com/envoyproxy/go-control-plane/envoy/service/cluster/v3"
	xdspb "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	edspb "github.com/envoyproxy/go-control-plane/envoy/service/endpoint/v3"
	"github.com/golang/protobuf/ptypes"
	structpb "github.com/golang/protobuf/ptypes/struct"
	"github.com/urfave/cli/v2"
)

func list(c *cli.Context) error {
	cl, err := New(c)
	if err != nil {
		return err
	}
	defer cl.Stop()

	if cl.dry {
		return nil
	}

	if c.NArg() > 0 {
		return listEndpoints(c)
	}

	dr := &xdspb.DiscoveryRequest{Node: cl.node}
	cds := cdspb.NewClusterDiscoveryServiceClient(cl.cc)
	resp, err := cds.FetchClusters(c.Context, dr)
	if err != nil {
		return err
	}

	clusters := []*clusterpb.Cluster{}
	for _, r := range resp.GetResources() {
		var any ptypes.DynamicAny
		if err := ptypes.UnmarshalAny(r, &any); err != nil {
			return err
		}
		if c, ok := any.Message.(*clusterpb.Cluster); !ok {
			continue
		} else {
			clusters = append(clusters, c)
		}
	}
	if len(clusters) == 0 {
		return fmt.Errorf("no clusters found")
	}

	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Name < clusters[j].Name })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, padding, ' ', 0)
	defer w.Flush()
	if c.Bool("H") {
		fmt.Fprintln(w, "CLUSTER\tVERSION\tHEALTHCHECKS\t")
	}
	for _, u := range clusters {
		hcs := u.GetHealthChecks()
		hcname := []string{}
		for _, hc := range hcs {
			x := fmt.Sprintf("%T", hc.HealthChecker)
			println("X", x)
			// get the prefix of the name of the type
			// HealthCheck_HttpHealthCheck_ --> Http
			// and supper case it.
			prefix := strings.Index(x, "HealthCheck_")
			if prefix == -1 || len(x) < 11 {
				continue
			}
			name := strings.ToUpper(x[prefix+12:]) // get the last bit
			name = name[:len(name)-12]             // remove HealthCheck_
			hcname = append(hcname, name)

		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t\n", u.GetName(), resp.GetVersionInfo(), strings.Join(hcname, Joiner), strings.Join(metadataToStringSlice(u.GetMetadata()), Joiner))
	}

	return nil
}

func listEndpoints(c *cli.Context) error {
	cl, err := New(c)
	if err != nil {
		return err
	}
	defer cl.Stop()

	if cl.dry {
		return nil
	}

	args := c.Args().Slice()
	cluster := ""
	switch len(args) {
	default:
		return ErrArg(args)
	case 1:
		cluster = args[0]
	}

	// We can't use resource names here, because the API then assumes we care about
	// these and it will have a watch for it; if we then ask again we don't get any replies if
	// there isn't any updates to the clusters. So keep ResourceNames empty and we filter
	// down below.
	dr := &xdspb.DiscoveryRequest{Node: cl.node}
	eds := edspb.NewEndpointDiscoveryServiceClient(cl.cc)
	resp, err := eds.FetchEndpoints(c.Context, dr)
	if err != nil {
		return err
	}

	endpoints := []*endpointpb.ClusterLoadAssignment{}
	for _, r := range resp.GetResources() {
		var any ptypes.DynamicAny
		if err := ptypes.UnmarshalAny(r, &any); err != nil {
			return err
		}
		if c, ok := any.Message.(*endpointpb.ClusterLoadAssignment); !ok {
			continue
		} else {
			if cluster != "" && cluster != c.ClusterName {
				continue
			}
			endpoints = append(endpoints, c)

		}
	}
	if len(endpoints) == 0 {
		return fmt.Errorf("no endpoints found")
	}

	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].ClusterName < endpoints[j].ClusterName })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, padding, ' ', 0)
	defer w.Flush()
	if c.Bool("H") {
		fmt.Fprintln(w, "CLUSTER\tVERSION\tENDPOINT\tLOCALITY\tHEALTH\tWEIGHT\t")
	}
	// we'll grab the data per localilty and then graph that. Locality is made up with Region/Zone/Subzone
	data := [][5]string{} // indexed by localilty and then numerical (0: name, 1: endpoints, 2: locality, 3: status, 4: weight)
	for _, e := range endpoints {
		for _, ep := range e.Endpoints {
			endpoints := []string{}
			healths := []string{}
			weights := []string{}
			for _, lb := range ep.GetLbEndpoints() {
				port := strconv.Itoa(int(lb.GetEndpoint().GetAddress().GetSocketAddress().GetPortValue()))
				endpoints = append(endpoints, net.JoinHostPort(lb.GetEndpoint().GetAddress().GetSocketAddress().GetAddress(), port))
				healths = append(healths, corepb.HealthStatus_name[int32(lb.GetHealthStatus())])
				weight := strconv.Itoa(int(lb.GetLoadBalancingWeight().GetValue()))
				weights = append(weights, weight)
			}
			locs := []string{}
			loc := ep.GetLocality()
			if x := loc.GetRegion(); x != "" {
				locs = append(locs, x)
			}
			if x := loc.GetZone(); x != "" {
				locs = append(locs, x)
			}
			if x := loc.GetSubZone(); x != "" {
				locs = append(locs, x)
			}
			where := strings.TrimSpace(strings.Join(locs, Joiner))

			data = append(data, [5]string{
				e.GetClusterName(),
				strings.Join(endpoints, Joiner),
				where,
				strings.Join(healths, Joiner),
				strings.Join(weights, Joiner),
			})
		}

	}
	for i := range data {
		d := data[i]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t\n", d[0], resp.GetVersionInfo(), d[1], d[2], d[3], d[4])

	}
	return nil
}

// metadataToStringSlice converts the corepb.Metadata's Fields to a string slice where
// each elements is FIELD:VALUE. VALUE's type must be structpb.Value_StringValue otherwise
// it will be skipped.
func metadataToStringSlice(m *corepb.Metadata) []string {
	if m == nil {
		return nil
	}
	fields := []string{}
	for _, v := range m.FilterMetadata {
		for k, v1 := range v.Fields {
			v2, ok := v1.Kind.(*structpb.Value_StringValue)
			if !ok {
				continue
			}
			fields = append(fields, k+":"+v2.StringValue)
		}
	}
	return fields
}

const Joiner = ","
