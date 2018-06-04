// Package datadog contains a Datadog exporter.
//
// This exporter is currently work in progress
package datadog

// import "go.opencensus.io/exporter/datadog"

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"sync"

	//"go.opencensus.io/internal"
	"github.com/DataDog/datadog-go/statsd"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

// Exporter exports stats to Datadog
type Exporter struct {
	opts Options
	c    *collector
	d    *statsd.Client
}

// Options contains options for configuring the exporter
type Options struct {
	// Namespace to prepend to all metrics
	Namespace string

	// Endpoint
	Endpoint string

	// OnError is the hook to be called when there is
	// an error occurred when uploading the stats data.
	// If no custom hook is set, errors are logged.
	// Optional.
	OnError func(err error)

	// Tags are global tags added to each metric
	Tags []string
}

var (
	newExporterOnce      sync.Once
	errSingletonExporter = errors.New("expecting only one exporter per instance")
)

// NewExporter returns an exporter that exports stats to Datadog
func NewExporter(o Options) (*Exporter, error) {
	var err = errSingletonExporter
	var exporter *Exporter
	newExporterOnce.Do(func() {
		exporter, err = newExporter(o)
	})
	return exporter, err
}

func newExporter(o Options) (*Exporter, error) {
	endpoint := o.Endpoint
	if endpoint == "" {
		endpoint = "127.0.0.1:8125"
	}

	fmt.Printf(endpoint)

	// client, err := statsd.New(o.Endpoint)
	client, err := statsd.New(endpoint)
	if err != nil {
		log.Fatal(err)
	}
	collector := newCollector(o)

	e := &Exporter{
		opts: o,
		c:    collector,
		d:    client,
	}
	return e, nil
}

// client implements datadog.Client
type collector struct {
	opts Options

	// mu guards all the fields.
	mu sync.Mutex

	skipErrors bool

	// viewData is accumulated and appended on every Export
	// invocation from stats.
	viewData map[string]*view.Data

	viewsMu sync.Mutex

	registeredViews map[string]string
}

func newCollector(o Options) *collector {
	return &collector{
		opts:            o,
		registeredViews: make(map[string]string),
		viewData:        make(map[string]*view.Data),
	}
}

// ExportView exports to Datadog if view data has one or more rows.
func (e *Exporter) ExportView(vd *view.Data) {
	if len(vd.Rows) == 0 {
		return
	}
	e.c.addViewData(vd, e.d)
}

func (c *collector) registerViews(views ...*view.View) {
	count := 0
	for _, view := range views {

		sig := viewSignature(c.opts.Namespace, view)
		c.viewsMu.Lock()
		_, ok := c.registeredViews[sig]
		c.viewsMu.Unlock()

		if !ok {
			metadata := view.Description
			c.viewsMu.Lock()
			c.registeredViews[sig] = metadata
			c.viewsMu.Unlock()
			count++
		}
	}
	if count == 0 {
		return
	}
}

func viewName(namespace string, v *view.View) string {
	var name string
	if namespace != "" {
		name = namespace + "."
	}
	//return name + internal.Sanitize(v.Name)
	return name + v.Name
}

func viewSignature(namespace string, v *view.View) string {
	var buf bytes.Buffer
	buf.WriteString(viewName(namespace, v))
	for _, k := range v.TagKeys {
		buf.WriteString("_" + k.Name())
	}
	return buf.String()
}

func (c *collector) addViewData(vd *view.Data, client *statsd.Client) {
	c.registerViews(vd.View)
	sig := viewSignature(c.opts.Namespace, vd.View)

	c.mu.Lock()
	c.viewData[sig] = vd
	c.mu.Unlock()

	for _, row := range vd.Rows {
		submitMetric(client, vd.View, row)
	}
}

func submitMetric(client *statsd.Client, v *view.View, row *view.Row) error {
	var tags []string
	tags = append(tags, "source:Opencensus")
	rate := 1
	var err error

	switch data := row.Data.(type) {
	case *view.CountData:
		return client.Gauge(v.Name, float64(data.Value), tagMetrics(row.Tags, tags), float64(rate))

	case *view.SumData:
		return client.Gauge(v.Name, float64(data.Value), tagMetrics(row.Tags, tags), float64(rate))

	case *view.LastValueData:
		return client.Gauge(v.Name, float64(data.Value), tagMetrics(row.Tags, tags), float64(rate))

	case *view.DistributionData:
		var metrics = map[string]float64{
			"min":             data.Min,
			"max":             data.Max,
			"count":           float64(data.Count),
			"avg":             data.Mean,
			"squared_dev_sum": data.SumOfSquaredDev,
		}

		for name, value := range metrics {
			err = client.Gauge(v.Name+"."+name, value, tagMetrics(row.Tags, tags), float64(rate))
		}

		for x := range data.CountPerBucket {
			bucketTags := append(tags, "bucket_idx"+fmt.Sprint(x))
			err = client.Gauge(v.Name+".count_per_bucket", float64(data.CountPerBucket[x]), tagMetrics(row.Tags, bucketTags), float64(rate))
		}
		return err
	default:
		return fmt.Errorf("aggregation %T is not supported", v.Aggregation)
	}
}

func tagMetrics(t []tag.Tag, ct []string) []string {
	var names, finaltag []string
	for _, tag := range t {
		names = append(names, tag.Key.Name())
	}
	for _, ctag := range ct {
		finaltag = append(names, ctag)
	}
	return finaltag
}

func (o *Options) onError(err error) {
	if o.OnError != nil {
		o.OnError(err)
	} else {
		log.Printf("Failed to export to Datadog: %v\n", err)
	}
}