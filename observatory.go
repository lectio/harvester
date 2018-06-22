package harvester

import (
	"io"
	"log"

	opentracing "github.com/opentracing/opentracing-go"
	metrics "github.com/uber/jaeger-lib/metrics"

	jaeger "github.com/uber/jaeger-client-go"
	jaegercfg "github.com/uber/jaeger-client-go/config"
	jaegerlog "github.com/uber/jaeger-client-go/log"
)

type ObserveeName string
type ObservatoryName string

type Observatory struct {
	tracer opentracing.Tracer
	config jaegercfg.Configuration
	closer io.Closer
}

func (o *Observatory) Close() {
	if o.closer != nil {
		o.closer.Close()
	}
}

func (o *Observatory) StartTrace(subject string) opentracing.Span {
	return o.tracer.StartSpan(subject)
}

func (o *Observatory) StartChildTrace(subject string, parent opentracing.Span) opentracing.Span {
	return o.tracer.StartSpan(subject, opentracing.ChildOf(parent.Context()))
}

func MakeTestObservatory(observer ObservatoryName, observee ObserveeName) *Observatory {
	result := new(Observatory)

	// Sample configuration for testing. Use constant sampling to sample every trace
	// and enable LogSpan to log every span via configured Logger.
	result.config = jaegercfg.Configuration{
		ServiceName: string(observee),
		Sampler: &jaegercfg.SamplerConfig{
			Type:  jaeger.SamplerTypeConst,
			Param: 1,
		},
		Reporter: &jaegercfg.ReporterConfig{
			LogSpans: true,
		},
	}

	// Example logger and metrics factory. Use github.com/uber/jaeger-client-go/log
	// and github.com/uber/jaeger-lib/metrics respectively to bind to real logging and metrics
	// frameworks.
	jLogger := jaegerlog.StdLogger
	jMetricsFactory := metrics.NullFactory

	// Initialize tracer with a logger and a metrics factory
	tracer, closer, err := result.config.NewTracer(
		jaegercfg.Logger(jLogger),
		jaegercfg.Metrics(jMetricsFactory),
	)
	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
	}
	result.tracer = tracer
	result.closer = closer
	return result
}

func MakeProductionObservatory(observer ObservatoryName, observee ObserveeName) *Observatory {
	result := new(Observatory)

	// Recommended configuration for production.
	result.config = jaegercfg.Configuration{}
	result.config.ServiceName = string(observee)

	// Example logger and metrics factory. Use github.com/uber/jaeger-client-go/log
	// and github.com/uber/jaeger-lib/metrics respectively to bind to real logging and metrics
	// frameworks.
	jLogger := jaegerlog.StdLogger
	jMetricsFactory := metrics.NullFactory

	// Initialize tracer with a logger and a metrics factory
	tracer, closer, err := result.config.NewTracer(
		jaegercfg.Logger(jLogger),
		jaegercfg.Metrics(jMetricsFactory),
	)
	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
	}
	result.tracer = tracer
	result.closer = closer
	return result
}

/*
TODO: see https://github.com/jaegertracing/jaeger-client-go/blob/master/config/example_test.go for
      other examples

func ExampleFromEnv() {
	cfg, err := jaegercfg.FromEnv()
	if err != nil {
		// parsing errors might happen here, such as when we get a string where we expect a number
		log.Printf("Could not parse Jaeger env vars: %s", err.Error())
		return
	}

	tracer, closer, err := cfg.NewTracer()
	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
		return
	}
	defer closer.Close()

	opentracing.SetGlobalTracer(tracer)
	// continue main()
}

func ExampleFromEnv_override() {
	os.Setenv("JAEGER_SERVICE_NAME", "not-effective")

	cfg, err := jaegercfg.FromEnv()
	if err != nil {
		// parsing errors might happen here, such as when we get a string where we expect a number
		log.Printf("Could not parse Jaeger env vars: %s", err.Error())
		return
	}

	cfg.ServiceName = "this-will-be-the-service-name"

	tracer, closer, err := cfg.NewTracer()
	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
		return
	}
	defer closer.Close()

	opentracing.SetGlobalTracer(tracer)
	// continue main()
}
*/
