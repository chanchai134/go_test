package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParser(t *testing.T) {
	data := []struct {
		name  string
		data  string
		input Input
		err   error
	}{
		{"+", "CALC_2\n+\n100\n3000", Input{"CALC_2", "+", 100, 3000}, nil},
		{"-", "CALC_2\n-\n100\nE000", Input{}, strconv.ErrSyntax},
		{"-", "CALC_2\n-\nN00\n3000", Input{}, strconv.ErrSyntax},
		{"OOR", "OOR\n+\n100\n10000000000000000000", Input{}, strconv.ErrRange},
		{"OOL4", "OOL\n+\n100", Input{}, ErrOol4},
	}

	for _, d := range data {
		t.Run(d.name, func(t *testing.T) {
			input, err := parser([]byte(d.data))
			if diff := cmp.Diff(d.input, input); diff != "" {
				t.Error(diff)
			}

			if !errors.Is(err, d.err) {
				t.Errorf("%v %v", err, d.err)
			}
		})
	}
}

func FuzzParser(f *testing.F) {
	f.Add([]byte("CALC_2\n+\n100\n3000"))
	f.Add([]byte("CALC_2\n+\n100\nE000"))
	f.Add([]byte("CALC_2\n+\nN00\n3000"))

	f.Fuzz(func(t *testing.T, in []byte) {
		input, err := parser(in)

		if err != nil {
			if errors.Is(err, strconv.ErrSyntax) || errors.Is(err, strconv.ErrRange) {
				t.Skip()
			} else if err.Error() == "input must have 4 lines" {
				t.Skip()
			} else {
				t.Error(err)
			}
		}

		roundTripInput := strings.Join([]string{input.Id, input.Op, strconv.Itoa(input.Val1), strconv.Itoa(input.Val2)}, "\n")
		input2, _ := parser([]byte(roundTripInput))

		if diff := cmp.Diff(input, input2); diff != "" {
			t.Error(diff)
		}
	})
}

func TestDataProcessor(t *testing.T) {
	data := []struct {
		name   string
		input  []byte
		output Result
	}{
		{"+", []byte("CALC_2\n+\n100\n3000"), Result{"CALC_2", 3100}},
		{"-", []byte("DD\n-\n100\n3000"), Result{"DD", -2900}},
		{"*", []byte("XX\n*\n100\n130"), Result{"XX", 13000}},
		{"/", []byte("hh\n/\n9\n2"), Result{"hh", 4}},
	}

	for _, d := range data {
		t.Run(d.name, func(t *testing.T) {
			in := make(chan []byte)
			out := make(chan Result)
			go DataProcessor(in, out)
			in <- d.input
			result := <-out
			if diff := cmp.Diff(result, d.output); diff != "" {
				t.Error(diff)
			}
			close(in)
		})
	}

	t.Run("continue cases", func(t *testing.T) {
		skipInput := []struct {
			name  string
			input []byte
		}{
			{"not 4 line", []byte("OOL\n+\n100")},
			{"unknown operator", []byte("hh\nXX\n9\n2")},
		}

		for _, d := range skipInput {
			t.Run(d.name, func(t *testing.T) {
				in := make(chan []byte)
				out := make(chan Result)
				go DataProcessor(in, out)
				in <- d.input
				in <- data[0].input
				result := <-out
				if diff := cmp.Diff(result, data[0].output); diff != "" {
					t.Error(diff)
				}
				close(in)
			})
		}
	})
}

func TestWriteData(t *testing.T) {
	in := make(chan Result)
	go func() {
		in <- Result{"hh", 4}
		close(in)
	}()
	var b bytes.Buffer
	WriteData(in, &b)
	out, err := io.ReadAll(&b)
	if err != nil {
		t.Error(err)
	}
	if diff := cmp.Diff(string(out), "hh:4\n"); diff != "" {
		t.Error(diff)
	}
}

func TestNewController(t *testing.T) {
	out := make(chan []byte, 5)
	controller := NewController(out)
	server := httptest.NewServer(controller)
	defer server.Close()

	success := make(chan int, 2000)
	fail := make(chan int, 2000)
	refuse := make(chan struct{}, 2000)
	client := server.Client()
	var wg sync.WaitGroup
	wg.Add(2000)
	for i := 0; i < 2000; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, strings.NewReader("ssss"))
			res, err := client.Do(req)
			if err != nil {
				refuse <- struct{}{}
				wg.Done()
				return
			}
			defer res.Body.Close()
			switch res.StatusCode {
			case http.StatusAccepted:
				s, _ := io.ReadAll(res.Body)
				g := bytes.Runes(s)
				x, _ := strconv.Atoi(string(g[4:]))
				success <- x
			default:
				s, _ := io.ReadAll(res.Body)
				g := bytes.Runes(s)
				x, _ := strconv.Atoi(string(g[10:]))
				fail <- x
			}
			wg.Done()
		}()
	}
	wg.Wait()
	close(success)
	close(fail)
	close(refuse)

	var total, s, f, r, maxF, maxS int

	for success != nil || fail != nil || refuse != nil {
		select {
		case x, ok := <-success:
			if !ok {
				success = nil
				continue
			}
			if x > maxS {
				maxS = x
			}
			s++
			total++
		case x, ok := <-fail:
			if !ok {
				fail = nil
				continue
			}
			if x > maxF {
				maxF = x
			}
			f++
			total++
		case _, ok := <-refuse:
			if !ok {
				refuse = nil
				continue
			}
			r++
			total++
		}
	}

	if diff := cmp.Diff(total, 2000); diff != "" {
		t.Error(diff)
	}

	if diff := cmp.Diff(s, 5); diff != "" {
		t.Error(diff)
	}

	if diff := cmp.Diff(maxS, 5); diff != "" {
		t.Error(diff)
	}

	if diff := cmp.Diff(f+r, 1995); diff != "" {
		t.Error(diff)
	}

	if diff := cmp.Diff(maxF, 1995-r); diff != "" {
		t.Error(diff)
	}
}
