//go:generate hel

package mapreduce_test

import (
	"fmt"
	"io"
	"testing"

	"github.com/apoydence/mapreduce"
	"github.com/apoydence/onpar"
	. "github.com/apoydence/onpar/expect"
	. "github.com/apoydence/onpar/matchers"
)

type TMR struct {
	*testing.T

	mockFileSystem *mockFileSystem
	mockNetwork    *mockNetwork

	mockFileReader *mockFileReader
}

func TestMapReduce(t *testing.T) {
	t.Parallel()
	o := onpar.New()
	defer o.Run(t)

	o.BeforeEach(func(t *testing.T) TMR {
		mockFileSystem := newMockFileSystem()
		mockNetwork := newMockNetwork()
		mockFileReader := newMockFileReader()

		return TMR{
			T:              t,
			mockFileSystem: mockFileSystem,
			mockNetwork:    mockNetwork,
			mockFileReader: mockFileReader,
		}
	})

	o.Group("when the FileSystem does not return an error", func() {
		o.BeforeEach(func(t TMR) TMR {
			t.mockFileSystem.ReadFileOutput.Ret0 <- t.mockFileReader
			close(t.mockFileSystem.ReadFileOutput.Ret1)

			return t
		})

		o.Group("when the FileSystem returns a single node", func() {
			o.BeforeEach(func(t TMR) TMR {
				t.mockFileSystem.NodesOutput.IDs <- []string{"some-id"}
				close(t.mockFileSystem.NodesOutput.Err)
				return t
			})

			o.Spec("it uses the correct name in the file system", func(t TMR) {
				v := make(chan []byte, 100)
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					v <- value
					return nil, false
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					return [][]byte{nil}
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

				writeToReader(t, "some-data")

				mr.Calculate("some-name")

				Expect(t, t.mockFileSystem.ReadFileInput.Name).To(ViaPolling(
					Chain(Receive(), Equal("some-name")),
				))

				Expect(t, v).To(ViaPolling(
					Chain(Receive(), Equal([]byte("some-data"))),
				))
			})

			o.Spec("it writes each functions result to the next function", func(t TMR) {
				r1 := make(chan [][]byte, 100)
				r2 := make(chan [][]byte, 100)
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					return []byte("some-key"), true
				})).Reduce(mapreduce.ReduceFunc(func(value [][]byte) (reduced [][]byte) {
					r1 <- value
					return [][]byte{{99}}
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					r2 <- value
					return [][]byte{nil}
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)
				writeToReader(t, "some-data")

				mr.Calculate("some-name")

				Expect(t, r1).To(ViaPolling(
					Chain(Receive(), Equal([][]byte{[]byte("some-data")})),
				))

				Expect(t, r2).To(ViaPolling(
					Chain(Receive(), Equal([][]byte{{99}})),
				))
			})

			o.Spec("it does not write filtered out data", func(t TMR) {
				called := make(chan bool, 100)
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					return nil, false
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					called <- true
					return [][]byte{nil}
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)
				writeToReader(t, "some-filtered-data", "some-data")

				mr.Calculate("some-name")

				Expect(t, called).To(Always(
					Not(Receive()),
				))
			})

			o.Spec("it groups the data via key", func(t TMR) {
				r := make(chan [][]byte, 100)
				var called int
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					called++
					if called%2 != 0 {
						return []byte("some-key-a"), true
					}
					return []byte("some-key-b"), true
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					r <- value
					return [][]byte{[]byte("some-reduced-data")}
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

				writeToReader(t, "some-data-1", "some-data-2", "some-data-3")

				mr.Calculate("some-name")
				Expect(t, r).To(ViaPolling(HaveLen(2)))

				var results [][][]byte
				for i := 0; i < 2; i++ {
					results = append(results, <-r)
				}

				Expect(t, results).To(Contain([][]byte{
					[]byte("some-data-1"),
					[]byte("some-data-3"),
				}))

				Expect(t, results).To(Contain([][]byte{
					[]byte("some-data-2"),
				}))
			})

			o.Spec("it invokes FinalReduce until result is length 1", func(t TMR) {
				called := make(chan bool, 100)
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					return []byte("a"), true
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					called <- true
					return value[1:]
				}))

				writeToReader(t, "some-data-1", "some-data-2", "some-data-3")

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

				mr.Calculate("some-name")

				Expect(t, called).To(ViaPolling(HaveLen(2)))
			})

			o.Spec("it returns a result tree", func(t TMR) {
				var called int
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					called++
					if called%2 != 0 {
						return []byte("some-key-a"), true
					}
					return []byte("some-key-b"), true
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					return value
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

				writeToReader(t, "some-data-1")

				results, err := mr.Calculate("some-name")
				Expect(t, err == nil).To(BeTrue())
				Expect(t, results == nil).To(BeFalse())

				_, isLeaf := results.Leaf()
				Expect(t, isLeaf).To(BeFalse())
				Expect(t, results.ChildrenKeys()).To(Contain(
					[]byte("some-key-a"),
				))
				child := results.Child([]byte("some-key-a"))
				Expect(t, child == nil).To(BeFalse())
				value, isLeaf := child.Leaf()
				Expect(t, isLeaf).To(BeTrue())
				Expect(t, string(value)).To(Equal("some-data-1"))
			})
		})

		o.Group("when the FileSystem returns multiple nodes", func() {
			o.BeforeEach(func(t TMR) TMR {
				t.mockFileSystem = newMockFileSystem()
				readers := []*mockFileReader{
					newMockFileReader(),
					newMockFileReader(),
					newMockFileReader(),
				}

				for i, r := range readers {
					r.ReadOutput.Ret0 <- []byte(fmt.Sprintf("some-data-%d", i))
					r.ReadOutput.Ret1 <- nil

					r.ReadOutput.Ret0 <- nil
					r.ReadOutput.Ret1 <- io.EOF

					t.mockFileSystem.ReadFileOutput.Ret0 <- r
					t.mockFileSystem.ReadFileOutput.Ret1 <- nil
				}
				t.mockFileSystem.LengthOutput.Length <- 3
				t.mockFileSystem.LengthOutput.Err <- nil

				t.mockFileSystem.NodesOutput.IDs <- []string{"1", "2", "3"}

				close(t.mockFileSystem.NodesOutput.Err)
				return t
			})

			o.Spec("it breaks up the calculation between the nodes", func(t TMR) {
				chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
					return []byte("a"), true
				})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
					return [][]byte{value[0]}
				}))

				mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

				results, err := mr.Calculate("some-name")
				Expect(t, err == nil).To(BeTrue())

				starts := toUint64Slice(t.mockFileSystem.ReadFileInput.Start)
				ends := toUint64Slice(t.mockFileSystem.ReadFileInput.End)

				Expect(t, starts).To(Contain(uint64(0), uint64(1), uint64(2)))
				Expect(t, ends).To(Contain(uint64(1), uint64(2), uint64(3)))

				_, isLeaf := results.Leaf()
				Expect(t, isLeaf).To(BeFalse())
				Expect(t, results.ChildrenKeys()).To(Contain(
					[]byte("a"),
				))
				child := results.Child([]byte("a"))
				Expect(t, child == nil).To(BeFalse())
				value, isLeaf := child.Leaf()
				Expect(t, isLeaf).To(BeTrue())
				Expect(t, string(value)).To(ContainSubstring("some-data-"))
			})
		})
	})

	o.Group("when the FileSystem returns an error", func() {
		o.BeforeEach(func(t TMR) TMR {
			t.mockFileSystem.LengthOutput.Length <- 0
			t.mockFileSystem.LengthOutput.Err <- fmt.Errorf("some-error")
			return t
		})

		o.Spec("it returns an error", func(t TMR) {
			chain := mapreduce.Build(mapreduce.MapFunc(func(value []byte) (key []byte, ok bool) {
				return nil, false
			})).FinalReduce(mapreduce.FinalReduceFunc(func(value [][]byte) (reduced [][]byte) {
				return value
			}))
			mr := mapreduce.New(t.mockFileSystem, t.mockNetwork, chain)

			_, err := mr.Calculate("some-name")
			Expect(t, err == nil).To(BeFalse())
		})
	})
}

func toUint64Slice(c chan uint64) []uint64 {
	var result []uint64
	for {
		select {
		case x := <-c:
			result = append(result, x)
		default:
			return result
		}
	}
}

func writeToReader(t TMR, data ...string) {
	for _, d := range data {
		t.mockFileReader.ReadOutput.Ret0 <- []byte(d)
		t.mockFileReader.ReadOutput.Ret1 <- nil
	}

	t.mockFileReader.ReadOutput.Ret0 <- nil
	t.mockFileReader.ReadOutput.Ret1 <- io.EOF

	t.mockFileSystem.LengthOutput.Length <- uint64(len(data))
	t.mockFileSystem.LengthOutput.Err <- nil
}
