package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
)

// Version of the program
const Version = "3.0-dev"

// Definition of the command line flags
var (
	flagWorkers  = flag.Int("workers", -1, "Number of workers. Default is 4*threads")
	flagThreads  = flag.Int("threads", -1, "Number of Go threads (i.e. GOMAXPROCS). Default is all OS processors")
	flagRun      = flag.Bool("run", false, "Run a single benchmark iteration. Mutually exclusive with -bench")
	flagBench    = flag.Bool("bench", false, "Run standard benchmark (multiple iterations). Mutually exclusive with -run")
	flagFreq     = flag.Bool("freq", false, "Measure the frequency of the CPU")
	flagDuration = flag.Int("duration", 60, "Duration in seconds of a single iteration")
	flagNb       = flag.Int("nb", 10, "Number of iterations")
	flagRes      = flag.String("res", "", "Optional result append file")
	flagVersion  = flag.Bool("version", false, "Display program version and exit")
	flagDebug    = flag.Bool("debug", false, "Show Debug info")
)

// main entry point of the progam
func main() {

	flag.Parse()

	if !*flagDebug {
		log.SetOutput(ioutil.Discard)
	}
	// Fix number of of threads of the Go runtime.
	// By default, 4 workers per thread.
	if *flagThreads == -1 {
		*flagThreads = runtime.NumCPU()
	}
	if *flagWorkers == -1 {
		*flagWorkers = *flagThreads * 4
	}
	runtime.GOMAXPROCS(*flagThreads)

	// Run a single iteration or a full benchmark
	var err error
	switch {
	case *flagRun:
		err = runBench()
	case *flagBench:
		err = stdBench()
	case *flagFreq:
		err = measureFreq()
	case *flagVersion:
		err = displayVersion()
	default:
		flag.Usage()
		os.Exit(-1)
	}

	if err != nil {
		log.Fatal(err)
	}

	os.Exit(0)
}

// runBench runs a simple benchmark
func runBench() error {

	log.Printf("CPU benchmark with %d threads and %d workers", *flagThreads, *flagWorkers)

	// We will maintain the workers busy by pre-filling a buffered channel
	init := make(chan WorkerOp, *flagWorkers)
	input := make(chan WorkerOp, *flagWorkers*32)
	output := make(chan int, *flagWorkers)

	log.Printf("Initializing workers")

	// Spawn workers and trigger initialization
	workers := []*Worker{}
	for i := 0; i < *flagWorkers; i++ {
		w := NewWorker(i, init, input, output)
		workers = append(workers, w)
		go w.Run()
		init <- OpInit
	}

	// Wait for all workers to be initialized
	for range workers {
		<-output
	}

	// Run a synchronous garbage collection now to avoid processing the garbage
	// associated to the initialization during the benchmark
	runtime.GC()
	runtime.GC()

	// Start the benchmark: it will run for a given duration
	log.Printf("Start")
	begin := time.Now()
	stop := make(chan bool)
	time.AfterFunc(time.Duration(*flagDuration)*time.Second, func() {
		log.Printf("Stop signal")
		stop <- true
	})

LOOP:
	// Benchmark loop: we avoid checking for the timeout too often
	for {
		select {
		case <-stop:
			break LOOP
		default:
			for i := 0; i < *flagWorkers*16; i++ {
				input <- OpStep
			}
		}
	}

	// Signal the end of the benchmark to workers, and aggregate results
	for range workers {
		input <- OpExit
	}
	nb := 0
	for range workers {
		nb += <-output
	}
	end := time.Now()
	log.Printf("End")

	// Calculate resulting throughput
	ns := float64(end.Sub(begin).Nanoseconds())
	res := float64(nb) * 1000000000.0 / ns
	log.Printf("THROUGHPUT %.6f", res)
	if !*flagDebug {
		fmt.Println(displayCPUVendorName())
		fmt.Printf("%.6f\n", res)
	}
	if *flagRes != "" {
		if err := AppendResult(*flagRes, *flagWorkers, res); err != nil {
			log.Print(err)
			log.Printf("Cannot write result into temporary file: %s", *flagRes)
		}
	}
	log.Print()

	return nil
}

// stdBench runs multiple benchmarks (single-threaded and then multi-threaded)
func stdBench() error {

	// Display CPU information
	log.Println("Version: ", Version)
	log.Print()
	if err := displayCPU(); err != nil {
		return nil
	}

	// Create a temporary file storing the results
	tmp, err := ioutil.TempFile("", "cpubench1a-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	// Run multiple benchmarks in sequence
	log.Print("Single threaded performance")
	log.Print("===========================")
	log.Print()
	for i := 0; i < *flagNb; i++ {
		if err := spawnBench(1, tmp.Name()); err != nil {
			return err
		}
	}

	// Run multiple benchmarks in sequence
	log.Print("Multi-threaded performance")
	log.Print("==========================")
	log.Print()
	for i := 0; i < *flagNb; i++ {
		if err := spawnBench(*flagWorkers, tmp.Name()); err != nil {
			return err
		}
	}

	// Display statistics from the temporary file
	DisplayResult(tmp, *flagWorkers)
	tmp.Close()
	return nil
}

// spawnBench runs a benchmark as an external process
func spawnBench(workers int, resfile string) error {
	// Get executable path
	executable, err := os.Executable()
	if err != nil {
		return nil
	}

	// Build parameters
	opt := []string{
		"-run",
		"-threads", strconv.Itoa(*flagThreads),
		"-workers", strconv.Itoa(workers),
		"-duration", strconv.Itoa(*flagDuration),
		"-nb", strconv.Itoa(*flagNb),
		"-res", resfile,
	}

	// Execute command in blocking mode
	cmd := exec.Command(executable, opt...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	return nil
}

func getCPUInfo(ctx context.Context) ([]cpu.InfoStat, error) {
	cpuInfo, err := cpu.InfoWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return cpuInfo, nil
}

// displayCPU displays some CPU information
func displayCPU() error {

	ctx := context.Background()

	// Get type of CPU, frequency
	cpuinfo, err := getCPUInfo(ctx)
	if err != nil {
		return err
	}

	log.Printf("CPU: %s / %s", cpuinfo[0].VendorID, cpuinfo[0].ModelName)
	log.Printf("Max freq: %.2f mhz (as reported by OS)", cpuinfo[0].Mhz)

	// The core/thread count is wrong on some architectures
	nc, err := cpu.CountsWithContext(ctx, false)
	if err != nil {
		return err
	}
	nt, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		return err
	}

	log.Printf("Cores: %d", nc)
	log.Printf("Threads: %d", nt)

	// Fetch NUMA topology
	numa := map[int]int{}
	if files, err := filepath.Glob("/sys/devices/system/node/node[0-9]*/cpu[0-9]*"); err == nil {
		for _, f := range files {
			t := strings.Split(strings.TrimPrefix(f, "/sys/devices/system/node/"), "/")
			if len(t) > 1 {
				var n, c int
				if n, err = strconv.Atoi(strings.TrimPrefix(t[0], "node")); err != nil {
					continue
				}
				if c, err = strconv.Atoi(strings.TrimPrefix(t[1], "cpu")); err != nil {
					continue
				}
				numa[c] = n
			}
		}
	}

	// Display NUMA topology
	for _, c := range cpuinfo {
		n, ok := numa[int(c.CPU)]
		if !ok {
			n = -1
		}
		log.Printf("CPU:%3d Socket:%3s CoreId:%3s Node:%3d", c.CPU, c.PhysicalID, c.CoreID, n)
	}
	log.Print()

	return nil
}

// measureFreq attempts to measure the CPU frequency by counting CPU cycles
func measureFreq() error {

	// First run to warm the CPU
	log.Println("Version:", Version)
	log.Println("Warming-up CPU")
	CountASM(NFREQ)
	log.Println("Measuring ...")

	// Second run to perform the actual measurement
	t := time.Now()
	CountASM(NFREQ)
	t2 := time.Now()
	dur := t2.Sub(t).Seconds()

	// The loop contains 1024 dependent instructions (1 cycle per instruction)
	// plus a test/jump (resulting in 1 or 2 additional cycles)
	log.Println("Frequency:", float64(NFREQ)/1024.0*ASMLoopCycles/dur/1.0e9, "GHz")

	return nil
}

// displayVersion just prints the program version
func displayVersion() error {
	fmt.Println("Version:", Version)
	return nil
}

func displayCPUVendorName() string {
	cpuinfo, _ := getCPUInfo(context.Background())
	return cpuinfo[0].ModelName
}
