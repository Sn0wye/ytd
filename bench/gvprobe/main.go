// gvprobe measures googlevideo throughput: it downloads the first N bytes of
// one stream with C connections and prints MB/s (download time only).
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Sn0wye/ytd/pkg/youtube"
)

func main() {
	id, itag := os.Args[1], os.Args[2]
	n, _ := strconv.ParseInt(os.Args[3], 10, 64)
	conns, _ := strconv.Atoi(os.Args[4])
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := youtube.NewClient(conns)
	v, err := c.GetVideo(ctx, id)
	if err != nil {
		fmt.Println("nan")
		os.Exit(1)
	}
	for _, f := range v.Formats {
		if strconv.Itoa(f.Itag) == itag && !f.DRC {
			f.Size = min(f.Size, n)
			out, _ := os.CreateTemp("", "gvprobe-")
			out.Close()
			defer os.Remove(out.Name())
			t := time.Now()
			if err := c.Download(ctx, []youtube.Job{{Format: f, Path: out.Name()}}, nil); err != nil {
				fmt.Println("nan")
				os.Exit(1)
			}
			fmt.Printf("%.3f\n", float64(f.Size)/time.Since(t).Seconds()/1e6)
			return
		}
	}
	fmt.Println("nan")
	os.Exit(1)
}
