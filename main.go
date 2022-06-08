package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type App struct {
	ListenAddress     string
	ListenPort        uint64
	AwairAddress      string
	TimeBetweenChecks time.Duration

	Logger *zap.Logger

	TempGauge                 prometheus.Gauge
	HumidityGauge             prometheus.Gauge
	Co2Gauge                  prometheus.Gauge
	VOCGauge                  prometheus.Gauge
	PM25Gauge                 prometheus.Gauge
	ScoreGauge                prometheus.Gauge
	DewPointGauge             prometheus.Gauge
	AbsoluteHumidityGauge     prometheus.Gauge
	Co2EstimateGauge          prometheus.Gauge
	Co2EstimateBaselinesGauge prometheus.Gauge
	VOCBaselineGauge          prometheus.Gauge
	VOCH2RawGauge             prometheus.Gauge
	VocEthanolRawGauge        prometheus.Gauge
	Pm10EstimateGauge         prometheus.Gauge
}

type AwairStats struct {
	Timestamp time.Time `json:"timestamp"`

	Score          int     `json:"score"`
	DewPoint       float64 `json:"dew_point"`
	Temp           float64 `json:"temp"`
	Humid          float64 `json:"humid"`
	AbsHumid       float64 `json:"abs_humid"`
	Co2            int     `json:"co2"`
	Co2Est         int     `json:"co2_est"`
	Co2EstBaseline int     `json:"co2_est_baseline"`
	Voc            int     `json:"voc"`
	VocBaseline    int     `json:"voc_baseline"`
	VocH2Raw       int     `json:"voc_h2_raw"`
	VocEthanolRaw  int     `json:"voc_ethanol_raw"`
	Pm25           int     `json:"pm25"`
	Pm10Est        int     `json:"pm10_est"`
}

func main() {
	_ctx, cancel := signal.NotifyContext(context.Background(), os.Kill, os.Interrupt)
	defer cancel()
	group, gctx := errgroup.WithContext(_ctx)

	rawLogger, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("Failed to start logger: %+v", err))
	}

	app := App{
		Logger: rawLogger,
	}

	// Initialize Flags for configuration
	pflag.StringVar(&app.ListenAddress, "listen", "0.0.0.0", "Listen address")
	pflag.Uint64Var(&app.ListenPort, "port", 2112, "Listen port number")
	pflag.StringVar(&app.AwairAddress, "awair-address", "http://localhost/air-data/latest", "Awair air-data URL")
	pflag.DurationVar(&app.TimeBetweenChecks, "poll-frequency", time.Second*30, "Duration to wait between polling device")
	pflag.Parse()

	// Initialize the Prometheus Gauges
	app.initializeGauges()
	http.Handle("/metrics", promhttp.Handler())

	group.Go(func() error {
		app.recordMetrics(gctx)
		return nil
	})

	listenString := fmt.Sprintf("%s:%d", app.ListenAddress, app.ListenPort)

	server := &http.Server{
		Addr: listenString,
	}

	group.Go(func() error {
		app.Logger.Info("Starting server", zap.String("listen", listenString))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("failed to start server: %+v", err)
		}
		return nil
	})

	app.Logger.Info("Awair Poller started", zap.String("listen_address", listenString), zap.String("awair_address", app.AwairAddress), zap.Duration("poll_frequency", app.TimeBetweenChecks))

	<-_ctx.Done()
	app.Logger.Info("Shutting down")

	cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	if err := server.Shutdown(cancelCtx); err != nil {
		app.Logger.Error("Error shutting down server", zap.Error(err))
	}

	if err := group.Wait(); err != nil {
		app.Logger.Error("Error shutting down", zap.Error(err))
	}

	app.Logger.Info("Shutdown complete")
}

func (app *App) initializeGauges() {
	app.TempGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "temp_c",
		Help:      "Dry bulb temperature (ºC)",
	})

	app.HumidityGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "relative_humidity",
		Help:      "Relative Humidity (%)",
	})

	app.Co2Gauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "co2_ppm",
		Help:      "Carbon Dioxide (ppm)",
	})

	app.VOCGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "voc_ppb",
		Help:      "Total Volatile Organic Compounds (ppb)",
	})

	app.PM25Gauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "pm25_ug_m3",
		Help:      "Particulate matter less than 2.5 microns in diameter (µg/m³)",
	})

	app.ScoreGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "score",
		Help:      "Awair Score (0-100)",
	})

	app.DewPointGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "dew_point_c",
		Help:      "The temperature at which water will condense and form into dew (ºC)",
	})

	app.AbsoluteHumidityGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "absolute_humidity",
		Help:      "Absolute Humidity (g/m³)",
	})

	app.Co2EstimateGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "co2_estimate",
		Help:      "Estimated Carbon Dioxide (ppm - calculated by the TVOC sensor)",
	})

	app.Co2EstimateBaselinesGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "co2_estimate_baselines",
		Help:      "A unitless value that represents the baseline from which the TVOC sensor partially derives its estimated (e)CO₂output.",
	})

	app.VOCBaselineGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "voc_baseline",
		Help:      "A unitless value that represents the baseline from which the TVOC sensor partially derives its TVOC output.",
	})

	app.VOCH2RawGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "voc_h2_raw",
		Help:      "A unitless value that represents the Hydrogen gas signal from which the TVOC sensor partially derives its TVOC output.",
	})

	app.VocEthanolRawGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "voc_ethanol_raw",
		Help:      "A unitless value that represents the Ethanol gas signal from which the TVOC sensor partially derives its TVOC output.",
	})

	app.Pm10EstimateGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "awair",
		Subsystem: "climate",
		Name:      "pm10_estimate",
		Help:      "Estimated particulate matter less than 10 microns in diameter (µg/m³ - calculated by the PM2.5 sensor)",
	})
}

func (app *App) recordMetrics(ctx context.Context) {
	ticker := time.NewTicker(app.TimeBetweenChecks)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			app.getAwairData(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (app *App) getAwairData(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.AwairAddress, nil)
	if err != nil {
		app.Logger.Error("Error creating request", zap.Error(err))
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		app.Logger.Error("Error getting data from awair", zap.Error(err))
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		app.Logger.Error("Error reading response body", zap.Error(err))
		return err
	}

	awairStats := AwairStats{}
	err = json.Unmarshal(body, &awairStats)
	if err != nil {
		app.Logger.Error("Error unmarshalling response body", zap.Error(err))
		return err
	}

	app.TempGauge.Set(awairStats.Temp)
	app.HumidityGauge.Set(awairStats.Humid)
	app.Co2Gauge.Set(float64(awairStats.Co2))
	app.VOCGauge.Set(float64(awairStats.Voc))
	app.PM25Gauge.Set(float64(awairStats.Pm25))
	app.ScoreGauge.Set(float64(awairStats.Score))
	app.DewPointGauge.Set(awairStats.DewPoint)
	app.AbsoluteHumidityGauge.Set(awairStats.AbsHumid)
	app.Co2EstimateGauge.Set(float64(awairStats.Co2Est))
	app.Co2EstimateBaselinesGauge.Set(float64(awairStats.Co2EstBaseline))
	app.VOCBaselineGauge.Set(float64(awairStats.VocBaseline))
	app.VOCH2RawGauge.Set(float64(awairStats.VocH2Raw))
	app.VocEthanolRawGauge.Set(float64(awairStats.VocEthanolRaw))
	app.Pm10EstimateGauge.Set(float64(awairStats.Pm10Est))

	app.Logger.Info("Successfully recorded metrics from Awair", zap.Any("metrics", awairStats))

	return nil
}
