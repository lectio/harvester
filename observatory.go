package harvester

import (
	"context"
	"io"
	"log"

	opentracing "github.com/opentracing/opentracing-go"

	jaegercfg "github.com/uber/jaeger-client-go/config"
)

type Observatory struct {
	tracer opentracing.Tracer
	config jaegercfg.Configuration
	closer io.Closer
}

func (o *Observatory) Tracer() opentracing.Tracer {
	return o.tracer
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

func (o *Observatory) StartTraceFromContext(ctx context.Context, operationName string, opts ...opentracing.StartSpanOption) (opentracing.Span, context.Context) {
	var span opentracing.Span
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		opts = append(opts, opentracing.ChildOf(parentSpan.Context()))
		span = o.tracer.StartSpan(operationName, opts...)
	} else {
		span = o.tracer.StartSpan(operationName, opts...)
	}
	return span, opentracing.ContextWithSpan(ctx, span)
}

func MakeObservatoryFromEnv() *Observatory {
	result := new(Observatory)

	cfg, err := jaegercfg.FromEnv()
	if err != nil {
		// parsing errors might happen here, such as when we get a string where we expect a number
		log.Printf("Could not parse Jaeger env vars: %s", err.Error())
		return result
	}
	result.config = *cfg

	tracer, closer, err := result.config.NewTracer()
	if err != nil {
		log.Printf("Could not initialize jaeger tracer: %s", err.Error())
		return result
	}
	result.tracer = tracer
	result.closer = closer
	return result
}
