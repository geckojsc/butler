package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-errors/errors"
	"github.com/itchio/butler/comm"
	"github.com/itchio/butler/netpool"
	"github.com/itchio/wharf/pwr"
	"github.com/itchio/wharf/sync"
	"github.com/itchio/wharf/tlc"
	"github.com/itchio/wharf/wire"
)

func ranges(patch string) {
	must(doRanges(patch))
}

type FakePool struct {
	container *tlc.Container
}

var _ sync.FilePool = (*FakePool)(nil)
var _ sync.WritableFilePool = (*FakePool)(nil)

func (fp *FakePool) GetReader(fileIndex int64) (io.Reader, error) {
	return fp.GetReadSeeker(fileIndex)
}

func (fp *FakePool) GetReadSeeker(fileIndex int64) (io.ReadSeeker, error) {
	return &NullReader{
		size: fp.container.Files[fileIndex].Size,
	}, nil
}

type NullReader struct {
	offset int64
	size   int64
}

func (nr *NullReader) Read(buf []byte) (int, error) {
	newOffset := nr.offset + int64(len(buf))
	if newOffset >= nr.size {
		newOffset = nr.size
	}

	readSize := int(newOffset - nr.offset)
	nr.offset = newOffset

	if readSize == 0 {
		return 0, io.EOF
	} else {
		return readSize, nil
	}
}

func (nr *NullReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case os.SEEK_END:
		nr.offset = nr.size + offset
	case os.SEEK_CUR:
		nr.offset += offset
	case os.SEEK_SET:
		nr.offset = offset
	}
	return nr.offset, nil
}

func (fp *FakePool) Close() error {
	return nil
}

type NopWriteCloser struct {
	writer io.Writer
}

func (nwc *NopWriteCloser) Write(buf []byte) (int, error) {
	return nwc.writer.Write(buf)
}

func (nwc *NopWriteCloser) Close() error {
	return nil
}

func (fp *FakePool) GetWriter(fileIndex int64) (io.WriteCloser, error) {
	return &NopWriteCloser{ioutil.Discard}, nil
}

func doRanges(patch string) error {
	patchStats, err := os.Lstat(patch)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	patchReader, err := os.Open(patch)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	rawPatchWire := wire.NewReadContext(patchReader)
	err = rawPatchWire.ExpectMagic(pwr.PatchMagic)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	header := &pwr.PatchHeader{}
	err = rawPatchWire.ReadMessage(header)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	patchWire, err := pwr.DecompressWire(rawPatchWire, header.Compression)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	targetContainer := &tlc.Container{}
	err = patchWire.ReadMessage(targetContainer)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	sourceContainer := &tlc.Container{}
	err = patchWire.ReadMessage(sourceContainer)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	comm.Opf("Showing ranges for %s patch", humanize.IBytes(uint64(patchStats.Size())))
	comm.Statf("Old version: %s in %s", humanize.IBytes(uint64(targetContainer.Size)), targetContainer.Stats())
	comm.Statf("New version: %s in %s", humanize.IBytes(uint64(sourceContainer.Size)), sourceContainer.Stats())
	deltaOp := "+"
	if sourceContainer.Size < targetContainer.Size {
		deltaOp = "-"
	}
	delta := math.Abs(float64(sourceContainer.Size - targetContainer.Size))
	comm.Statf("Delta: %s%s (%s%.2f%%)", deltaOp, humanize.IBytes(uint64(delta)), deltaOp, delta/float64(targetContainer.Size)*100.0)
	comm.Log("")

	numDatas := 0
	numBlockRanges := 0
	blockSize := int64(pwr.BlockSize)
	bigBlockSize := *appArgs.bigBlockSize

	unchangedBytes := int64(0)
	movedBytes := int64(0)
	freshBytes := int64(0)

	requiredOldBlocks := make([]map[int64]bool, len(targetContainer.Files))
	for i := 0; i < len(targetContainer.Files); i++ {
		requiredOldBlocks[i] = make(map[int64]bool)
	}

	sh := &pwr.SyncHeader{}
	for fileIndex, sourceFile := range sourceContainer.Files {
		sh.Reset()
		err := patchWire.ReadMessage(sh)
		if err != nil {
			return errors.Wrap(err, 1)
		}

		if sh.FileIndex != int64(fileIndex) {
			fmt.Printf("expected fileIndex = %d, got fileIndex %d\n", fileIndex, sh.FileIndex)
			return errors.Wrap(pwr.ErrMalformedPatch, 1)
		}

		rop := &pwr.SyncOp{}

		err = (func() error {
			sourceOffset := int64(0)
			numMoved := 0
			numUnchanged := 0
			numFresh := 0

			for {
				rop.Reset()
				pErr := patchWire.ReadMessage(rop)
				if pErr != nil {
					return errors.Wrap(pErr, 1)
				}

				switch rop.Type {
				case pwr.SyncOp_BLOCK_RANGE:
					targetOffset := blockSize * rop.BlockIndex
					targetFile := targetContainer.Files[rop.FileIndex]

					size := blockSize * rop.BlockSpan

					alignedSize := blockSize * (rop.BlockIndex + rop.BlockSpan)
					// op includes last block which is smaller than blockSize
					if alignedSize > targetFile.Size {
						size -= blockSize
						size += targetFile.Size % blockSize
					}

					if targetFile.Path == sourceFile.Path && targetOffset == sourceOffset {
						// comm.Statf("%d unchanged blocks %d bytes into %s", rop.BlockSpan, sourceOffset, targetFile.Path)
						unchangedBytes += size
						numUnchanged++
					} else {
						movedBytes += size
						numMoved++

						bigBlockStart := int64(math.Floor(float64(rop.BlockIndex*blockSize) / float64(bigBlockSize)))
						bigBlockEnd := int64(math.Ceil(float64(rop.BlockIndex*blockSize+size) / float64(bigBlockSize)))

						for i := bigBlockStart; i < bigBlockEnd; i++ {
							requiredOldBlocks[rop.FileIndex][i] = true
						}
					}

					numBlockRanges++
					sourceOffset += size
				case pwr.SyncOp_DATA:
					size := int64(len(rop.Data))
					sourceOffset += size
					freshBytes += size
					numDatas++
					numFresh++
				case pwr.SyncOp_HEY_YOU_DID_IT:
					if numFresh == 0 && numUnchanged == 0 && numMoved == 1 {
						comm.Statf("Found rename!")
					} else {
						// comm.Statf("Fresh %d, unchanged %d, moved %d", numFresh, numUnchanged, numMoved)
					}
					return nil
				}
			}
		})()
		if err != nil {
			return errors.Wrap(err, 1)
		}
	}

	comm.Statf("%d BlockRange ops, %d Data ops", numBlockRanges, numDatas)
	comm.Statf("Unchanged bytes: %s", humanize.IBytes(uint64(unchangedBytes)))
	comm.Statf("Moved bytes    : %s", humanize.IBytes(uint64(movedBytes)))
	comm.Statf("Fresh bytes    : %s", humanize.IBytes(uint64(freshBytes)))

	comm.Log("")

	totalBlocks := 0
	partialBlocks := 0
	neededBlocks := 0
	neededBlockSize := int64(0)

	httpSourcePort := 23004
	blockAddresses := make(netpool.BlockAddressMap)

	for i, blockMap := range requiredOldBlocks {
		f := targetContainer.Files[i]
		fileNumBlocks := int64(math.Ceil(float64(f.Size) / float64(bigBlockSize)))
		for j := int64(0); j < fileNumBlocks; j++ {
			totalBlocks++
			if blockMap[j] {
				size := bigBlockSize
				if (j+1)*bigBlockSize > f.Size {
					partialBlocks++
					size = f.Size % bigBlockSize
				}
				neededBlockSize += size
				neededBlocks++
				address := fmt.Sprintf("shake128-32/fakehash-%d-%d/%d", i, j, size)
				blockAddresses.Set(int64(i), j, address)
			}
		}
	}
	comm.Statf("Total old blocks: %d, needed: %d (of which %d are smaller than %s)", totalBlocks, neededBlocks, partialBlocks, humanize.IBytes(uint64(bigBlockSize)))
	comm.Statf("Needed block size: %s (%.2f%% of full old build size)", humanize.IBytes(uint64(neededBlockSize)), float64(neededBlockSize)/float64(targetContainer.Size)*100.0)

	fullZeroBlock := make([]byte, bigBlockSize)
	simulatedLatency := time.Duration(*rangesArgs.latency) * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc("/blocks/", func(w http.ResponseWriter, r *http.Request) {
		tokens := strings.Split(strings.TrimLeft(r.URL.String(), "/blocks/"), "/")
		last := tokens[len(tokens)-1]
		size, pErr := strconv.ParseInt(last, 10, 64)
		if pErr != nil {
			comm.Warnf("Invalid URL requested", r.URL.String())
			http.Error(w, "Invalid size", 400)
			return
		}

		time.Sleep(simulatedLatency)
		// comm.Logf("Serving %s", r.URL.String())

		_, hErr := w.Write(fullZeroBlock[0:size])
		if hErr != nil {
			panic(hErr)
		}
	})
	go func() {
		httpErr := http.ListenAndServe(fmt.Sprintf(":%d", httpSourcePort), mux)
		if httpErr != nil {
			panic(httpErr)
		}
	}()

	targetPool := &netpool.NetPool{
		Container:      targetContainer,
		BlockSize:      bigBlockSize,
		BlockAddresses: blockAddresses,

		Upstream: &netpool.HttpSource{
			BaseURL: fmt.Sprintf("http://localhost:%d/blocks", httpSourcePort),
		},

		Consumer: comm.NewStateConsumer(),
	}

	actx := &pwr.ApplyContext{
		Consumer:   comm.NewStateConsumer(),
		TargetPool: targetPool,
	}

	if *rangesArgs.writeToDisk {
		actx.OutputPath = "out"
	} else {
		actx.OutputPool = &FakePool{sourceContainer}
	}

	_, err = patchReader.Seek(0, os.SEEK_SET)
	if err != nil {
		return err
	}

	startTime := time.Now()

	comm.StartProgress()
	err = actx.ApplyPatch(patchReader)
	if err != nil {
		return err
	}
	comm.EndProgress()

	totalTime := time.Since(startTime)
	comm.Statf("Processed in %s (%s/s)", totalTime, humanize.IBytes(uint64(float64(targetContainer.Size)/totalTime.Seconds())))

	return nil
}