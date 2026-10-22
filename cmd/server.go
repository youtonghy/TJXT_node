package cmd

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/youtonghy/TJXT_node/conf"
	"github.com/youtonghy/TJXT_node/core"
	"github.com/youtonghy/TJXT_node/limiter"
	"github.com/youtonghy/TJXT_node/node"
)

var (
	config string
	watch  bool
)

// Backoff between attempts to start again after a reload tore the old
// instance down and the new one failed to start.
const (
	restartBackoffMin = time.Second
	restartBackoffMax = time.Minute
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run the tjxt-node server",
	RunE:  serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/tjxt-node/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	command.AddCommand(&serverCommand)
}

// instance is one running generation of nodes and their Xray core.
type instance struct {
	nodes *node.Node
	core  *core.V2Core
}

// prepared is a loaded and validated configuration whose node info has been
// fetched and whose Xray config builds, ready to replace the running instance.
type prepared struct {
	conf  *conf.Conf
	nodes *node.Node
}

func serverHandle(_ *cobra.Command, _ []string) error {
	showVersion()
	log.SetFormatter(&log.TextFormatter{
		DisableTimestamp: true,
		DisableQuote:     true,
		PadLevelText:     false,
	})
	//init limiter
	limiter.Init()
	// A config file that does not load is the only startup error that exits:
	// an unreachable panel or a rejected key is retried below while the
	// process stays up.
	c, err := loadConfig(config)
	if err != nil {
		return err
	}
	applyLogConfig(c)
	// Enable pprof if configured
	if c.PprofPort != 0 {
		go func() {
			log.Infof("Starting pprof server on :%d", c.PprofPort)
			if err := http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", c.PprofPort), nil); err != nil {
				log.WithField("err", err).Error("pprof server failed")
			}
		}()
	}
	var reloadCh = make(chan struct{}, 1)

	// running is the configuration of current. retry re-attempts a start or
	// reload that failed: while the old (or rolled-back) instance keeps
	// serving, or while nothing runs (the first start or a rollback failed).
	var (
		current *instance
		running *prepared
		retry   <-chan time.Time
	)
	backoff := restartBackoffMin
	scheduleRetry := func(err error) {
		log.WithField("err", err).Errorf("重启失败, retrying in %s", backoff)
		retry = time.After(backoff)
		backoff = min(backoff*2, restartBackoffMax)
	}
	next, err := prepareConf(c, nil)
	if err == nil {
		current, err = next.start(reloadCh)
	}
	if err != nil {
		log.WithField("err", err).Error("Start nodes failed, retrying")
		scheduleRetry(err)
	} else {
		running = next
		logStarted(next)
	}
	if watch {
		if _, err := os.Stat(config); err != nil {
			// Only the environment configures the node (e.g. Docker).
			log.WithField("err", err).Info("Config file not readable, not watching it")
		} else {
			// On file change, just signal reload; do not run reload concurrently here
			w, err := conf.Watch(config, func() {
				select {
				case reloadCh <- struct{}{}:
				default: // drop if a reload is already queued
				}
			})
			if err != nil {
				current.close()
				return fmt.Errorf("start watch: %w", err)
			}
			defer func() {
				if err := w.Close(); err != nil {
					log.WithField("err", err).Error("Close config watcher failed")
				}
			}()
		}
	}
	// clear memory
	runtime.GC()

	osSignals := make(chan os.Signal, 1)
	signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)

	attemptReload := func() {
		retry = nil
		var prev *node.Node
		if running != nil {
			prev = running.nodes
		}
		next, err := prepare(config, prev)
		if err != nil {
			if current != nil {
				log.Warn("new config rejected, keeping the running one")
			}
			scheduleRetry(err)
			return
		}
		applyLogConfig(next.conf)
		current.close()
		current = nil
		if current, err = next.start(reloadCh); err == nil {
			log.Info("重启成功")
			logStarted(next)
			running = next
			backoff = restartBackoffMin
			runtime.GC()
			return
		}
		startErr := err
		if running != nil {
			// The old instance is gone already; bring the last working
			// configuration back from its cached node info.
			if inst, again, rerr := running.restart(reloadCh); rerr == nil {
				current, running = inst, again
				log.WithField("err", startErr).Error("new config failed to start, rolled back to the previous one")
			} else {
				log.WithField("err", rerr).Error("rollback to the previous config failed")
			}
		}
		scheduleRetry(startErr)
	}
	for {
		select {
		case <-osSignals:
			log.Info("收到退出信号，正在关闭程序...")
			// Closing the nodes flushes unreported traffic to the panel.
			current.close()
			return nil
		case <-reloadCh:
			if retry != nil {
				// A failed reload is already scheduled with backoff; polls that keep
				// seeing the (still broken) new config must not bypass it.
				log.Debug("reload requested while a retry is pending, waiting for it")
				continue
			}
			log.Info("收到重启信号，正在重新加载配置...")
			attemptReload()
		case <-retry:
			attemptReload()
		}
	}
}

// loadConfig loads and validates the config file (and the environment node).
func loadConfig(path string) (*conf.Conf, error) {
	c := conf.New()
	if err := c.LoadFromPath(path); err != nil {
		return nil, fmt.Errorf("load config %s: %w", path, err)
	}
	return c, nil
}

// prepare loads, validates and resolves a configuration without touching the
// running instance. prev is the running generation of nodes, if any.
func prepare(path string, prev *node.Node) (*prepared, error) {
	c, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	return prepareConf(c, prev)
}

// prepareConf fetches the node configs and checks that the Xray config builds.
// Nodes whose config cannot be fetched are left pending rather than failing
// the whole configuration.
func prepareConf(c *conf.Conf, prev *node.Node) (*prepared, error) {
	nodes, err := node.New(c.NodeConfigs, prev)
	if err != nil {
		return nil, fmt.Errorf("get node info: %w", err)
	}
	if _, err := core.BuildConfig(c, nodes.NodeInfos); err != nil {
		return nil, fmt.Errorf("build xray config: %w", err)
	}
	return &prepared{conf: c, nodes: nodes}, nil
}

func logStarted(p *prepared) {
	if pending := p.nodes.Pending(); pending > 0 {
		log.Warnf("Started %d nodes, %d wait for their panel config and start once it arrives", len(p.nodes.Configs), pending)
		return
	}
	log.Infof("Started %d nodes", len(p.nodes.Configs))
}

// start starts the core and the nodes; on failure everything started is
// closed again.
func (p *prepared) start(reloadCh chan struct{}) (*instance, error) {
	v2core := core.New(p.conf)
	v2core.ReloadCh = reloadCh
	if err := v2core.Start(p.nodes.NodeInfos); err != nil {
		return nil, fmt.Errorf("start core: %w", err)
	}
	inst := &instance{nodes: p.nodes, core: v2core}
	if err := p.nodes.Start(v2core); err != nil {
		inst.close()
		return nil, fmt.Errorf("start nodes: %w", err)
	}
	return inst, nil
}

// restart starts the same configuration again with fresh controllers built
// from the cached node info, without asking the panel.
func (p *prepared) restart(reloadCh chan struct{}) (*instance, *prepared, error) {
	nodes, err := p.nodes.Rebuild()
	if err != nil {
		return nil, nil, err
	}
	again := &prepared{conf: p.conf, nodes: nodes}
	inst, err := again.start(reloadCh)
	if err != nil {
		return nil, nil, err
	}
	return inst, again, nil
}

// close stops the nodes (flushing traffic) and then the core, logging errors.
func (i *instance) close() {
	if i == nil {
		return
	}
	if err := i.nodes.Close(); err != nil {
		log.WithField("err", err).Error("Close nodes failed")
	}
	if err := i.core.Close(); err != nil {
		log.WithField("err", err).Error("Close core failed")
	}
}

func applyLogConfig(c *conf.Conf) {
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn", "warning":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if c.LogConfig.Output == "" {
		return
	}
	f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.WithField("err", err).Error("Open log file failed, keeping the current log output")
		return
	}
	old := log.StandardLogger().Out
	log.SetOutput(f)
	// 关闭旧的日志文件（如果是文件）
	if oldFile, ok := old.(*os.File); ok && oldFile != os.Stdout && oldFile != os.Stderr {
		if err := oldFile.Close(); err != nil {
			log.WithField("err", err).Warn("Close previous log file failed")
		}
	}
}
