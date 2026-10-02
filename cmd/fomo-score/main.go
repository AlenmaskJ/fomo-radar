package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/alen1/fomo-radar/internal/domain"
	"github.com/alen1/fomo-radar/internal/lab"
	"github.com/alen1/fomo-radar/internal/score"
	"github.com/alen1/fomo-radar/internal/store"
)

const maxConfigBytes = 1 << 20

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("需要子命令：list、add、disable、compare 或 promote")
	}
	switch args[0] {
	case "list":
		return runList(args[1:], getenv, stdout, stderr)
	case "add":
		return runAdd(args[1:], getenv, stdout, stderr)
	case "disable":
		return runDisable(args[1:], getenv, stdout, stderr)
	case "compare":
		return runCompare(args[1:], getenv, stdout, stderr)
	case "promote":
		return runPromote(args[1:], getenv, stdout, stderr)
	default:
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

func runList(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := commandFlags("list", stderr)
	dbPath := flags.String("db", envOr(getenv, "FOMO_DB", "./fomo.db"), "Core 数据库路径")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	database, err := openCore(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close() //nolint:errcheck
	assignments, err := database.ScoreAssignments(context.Background())
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "版本\t角色\t配置摘要\t注册时间(UTC)\t角色变更时间(UTC)")
	for _, assignment := range assignments {
		sum := sha256.Sum256(assignment.ConfigJSON)
		fmt.Fprintf(stdout, "%s\t%s\t%x\t%s\t%s\n",
			assignment.Version, assignment.Role, sum[:6],
			assignment.CreatedAt.UTC().Format(time.RFC3339), assignment.ChangedAt.UTC().Format(time.RFC3339))
	}
	return nil
}

func runAdd(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := commandFlags("add", stderr)
	dbPath := flags.String("db", envOr(getenv, "FOMO_DB", "./fomo.db"), "Core 数据库路径")
	configPath := flags.String("config", "", "Challenger 配置文件")
	reason := flags.String("reason", "", "操作原因")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" {
		return errors.New("add 需要 --config")
	}
	if strings.TrimSpace(*reason) == "" {
		return errors.New("add 需要 --reason")
	}
	engine, configJSON, err := readScoreConfig(*configPath)
	if err != nil {
		return err
	}
	database, err := openCore(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close() //nolint:errcheck
	changed, err := database.EnableChallenger(context.Background(), engine.Version(), configJSON, *reason, time.Now().UTC())
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(stdout, "%s 已是活跃 Challenger，无变更。\n", engine.Version())
		return nil
	}
	fmt.Fprintf(stdout, "已启用 Challenger：%s\n", engine.Version())
	return nil
}

func runDisable(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := commandFlags("disable", stderr)
	dbPath := flags.String("db", envOr(getenv, "FOMO_DB", "./fomo.db"), "Core 数据库路径")
	version := flags.String("version", "", "Challenger 版本")
	reason := flags.String("reason", "", "操作原因")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if strings.TrimSpace(*version) == "" {
		return errors.New("disable 需要 --version")
	}
	if strings.TrimSpace(*reason) == "" {
		return errors.New("disable 需要 --reason")
	}
	database, err := openCore(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close() //nolint:errcheck
	changed, err := database.DisableChallenger(context.Background(), *version, *reason, time.Now().UTC())
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(stdout, "%s 已停用，无变更。\n", *version)
		return nil
	}
	fmt.Fprintf(stdout, "已停用 Challenger：%s\n", *version)
	return nil
}

func runPromote(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := commandFlags("promote", stderr)
	dbPath := flags.String("db", envOr(getenv, "FOMO_DB", "./fomo.db"), "Core 数据库路径")
	version := flags.String("version", "", "目标 Challenger 版本")
	expected := flags.String("expected-champion", "", "预期当前 Champion")
	reason := flags.String("reason", "", "操作原因")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	if strings.TrimSpace(*version) == "" {
		return errors.New("promote 需要 --version")
	}
	if strings.TrimSpace(*expected) == "" {
		return errors.New("promote 需要 --expected-champion")
	}
	if strings.TrimSpace(*reason) == "" {
		return errors.New("promote 需要 --reason")
	}
	database, err := openCore(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close() //nolint:errcheck
	eventID, err := database.PromoteChallenger(context.Background(), *version, *expected, *reason, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "晋升成功：%s -> %s，审计事件 #%d\n", *expected, *version, eventID)
	return nil
}

func runCompare(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := commandFlags("compare", stderr)
	dbPath := flags.String("db", envOr(getenv, "FOMO_DB", "./fomo.db"), "Core 数据库路径")
	labPath := flags.String("lab-db", envOr(getenv, "FOMO_LAB_DB", "./fomo-lab.db"), "Signal Lab 数据库路径")
	if err := parseFlags(flags, args); err != nil {
		return err
	}

	database, err := openCore(*dbPath)
	if err != nil {
		return err
	}
	assignments, err := database.ActiveScoreAssignments(context.Background())
	closeErr := database.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	source, err := lab.OpenSource(*dbPath)
	if err != nil {
		return err
	}
	defer source.Close() //nolint:errcheck
	repository, err := lab.OpenRepository(*labPath)
	if err != nil {
		return err
	}
	defer repository.Close() //nolint:errcheck
	state, err := repository.State(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("Signal Lab 尚未初始化")
	}
	if err != nil {
		return err
	}
	if state.CanonicalSourcePath != source.CanonicalPath() {
		return errors.New("Signal Lab 绑定的 Core 数据库路径不匹配")
	}
	identity, err := source.Identity(context.Background(), state.ActivationSnapshotID)
	if err != nil {
		return err
	}
	if identity != state.SourceDBIdentity {
		return errors.New("Signal Lab 绑定的 Core 数据库身份不匹配")
	}
	versions := make([]string, 0, len(assignments))
	roles := make(map[string]domain.ScoreRole, len(assignments))
	champion := ""
	for _, assignment := range assignments {
		versions = append(versions, assignment.Version)
		roles[assignment.Version] = assignment.Role
		if assignment.Role == domain.ScoreRoleChampion {
			champion = assignment.Version
		}
	}
	comparison, err := repository.LatestComparison(context.Background(), versions)
	if err != nil {
		return err
	}
	writeComparison(stdout, comparison, roles, champion)
	return nil
}

func openCore(path string) (*store.Store, error) {
	database, err := store.Open(path)
	if err != nil {
		return nil, err
	}
	configJSON, err := score.NewEngine().ConfigJSON()
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := database.BootstrapChampion(context.Background(), score.Version, configJSON, time.Now().UTC()); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func readScoreConfig(path string) (score.Engine, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return score.Engine{}, nil, fmt.Errorf("读取评分配置：%w", err)
	}
	defer file.Close() //nolint:errcheck
	raw, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return score.Engine{}, nil, fmt.Errorf("读取评分配置：%w", err)
	}
	if len(raw) > maxConfigBytes {
		return score.Engine{}, nil, fmt.Errorf("评分配置超过 %d 字节上限", maxConfigBytes)
	}
	engine, err := score.ParseConfig(raw)
	if err != nil {
		return score.Engine{}, nil, err
	}
	canonical, err := engine.ConfigJSON()
	if err != nil {
		return score.Engine{}, nil, err
	}
	return engine, canonical, nil
}

func commandFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("多余参数：%s", strings.Join(flags.Args(), " "))
	}
	return nil
}

func envOr(getenv func(string) string, key, fallback string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return fallback
}

type comparisonKey struct {
	Source, Chain, Evidence string
	Threshold               int
	Horizon                 string
}

func writeComparison(output io.Writer, comparison lab.ComparisonReport, roles map[string]domain.ScoreRole, champion string) {
	statistics := append([]lab.ThresholdStatistic(nil), comparison.Statistics...)
	sort.Slice(statistics, func(i, j int) bool {
		a, b := statistics[i], statistics[j]
		left := fmt.Sprintf("%s\x00%s\x00%s\x00%06d\x00%s\x00%s", a.SignalSourceScope, a.ChainScope, a.EvidenceScope, a.Threshold, a.Horizon, a.ScoreVersion)
		right := fmt.Sprintf("%s\x00%s\x00%s\x00%06d\x00%s\x00%s", b.SignalSourceScope, b.ChainScope, b.EvidenceScope, b.Threshold, b.Horizon, b.ScoreVersion)
		return left < right
	})
	championCells := map[comparisonKey]lab.ThresholdStatistic{}
	for _, statistic := range statistics {
		if statistic.ScoreVersion == champion {
			championCells[statisticKey(statistic)] = statistic
		}
	}
	fmt.Fprintf(output, "分析 #%d，生成时间 %s，数据高水位 %d\n", comparison.Run.ID, comparison.Run.GeneratedAt.UTC().Format(time.RFC3339), comparison.Run.PersistedThroughSnapshotID)
	fmt.Fprintln(output, "来源\t链\t证据\t阈值\t周期\t版本\t角色\t信号\t可入场\t不可入场\t待成熟\t成熟\t数据不足\t入场覆盖\t成熟覆盖\t结果覆盖\t正收益率\t中位收益\tMFE\tMAE\tΔ正收益率\tΔ中位收益\tΔMFE\tΔMAE\t样本")
	for _, statistic := range statistics {
		deltaPositive, deltaReturn, deltaMFE, deltaMAE := "N/A", "N/A", "N/A", "N/A"
		if roles[statistic.ScoreVersion] == domain.ScoreRoleChallenger {
			if baseline, ok := championCells[statisticKey(statistic)]; ok {
				deltaPositive = formatDelta(statistic.PositiveReturnRate, baseline.PositiveReturnRate)
				deltaReturn = formatDelta(statistic.MedianReturn, baseline.MedianReturn)
				deltaMFE = formatDelta(statistic.MedianMFE, baseline.MedianMFE)
				deltaMAE = formatDelta(statistic.MedianMAE, baseline.MedianMAE)
			}
		}
		sample := "OK"
		if statistic.InsufficientSample {
			sample = "INSUFFICIENT"
		}
		fmt.Fprintf(output, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			statistic.SignalSourceScope, statistic.ChainScope, statistic.EvidenceScope,
			statistic.Threshold, statistic.Horizon, statistic.ScoreVersion, roles[statistic.ScoreVersion],
			statistic.Signals, statistic.EvaluableEntries, statistic.NotEvaluable, statistic.Pending,
			statistic.Matured, statistic.InsufficientData,
			formatOptional(statistic.EntryPriceCoverage), formatOptional(statistic.MaturityCoverage), formatOptional(statistic.ResultCoverage),
			formatOptional(statistic.PositiveReturnRate), formatOptional(statistic.MedianReturn),
			formatOptional(statistic.MedianMFE), formatOptional(statistic.MedianMAE),
			deltaPositive, deltaReturn, deltaMFE, deltaMAE, sample)
	}
}

func statisticKey(statistic lab.ThresholdStatistic) comparisonKey {
	return comparisonKey{
		Source: statistic.SignalSourceScope, Chain: statistic.ChainScope,
		Evidence: statistic.EvidenceScope, Threshold: statistic.Threshold, Horizon: statistic.Horizon,
	}
}

func formatOptional(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.4f", *value)
}

func formatDelta(value, baseline *float64) string {
	if value == nil || baseline == nil {
		return "N/A"
	}
	return fmt.Sprintf("%+.4f", *value-*baseline)
}
