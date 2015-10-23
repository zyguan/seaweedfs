package directory

import (
	"encoding/gob"
	"errors"
	"log"
	"math/rand"
	"os"
	"path"
	"strconv"
	"sync"

	"github.com/zyguan/seaweedfs/lib/storage"
)

const (
	FileIdSaveInterval = 10000
)

type MachineInfo struct {
	Url       string //<server name/ip>[:port]
	PublicUrl string
}
type Machine struct {
	Server  MachineInfo
	Volumes []storage.VolumeInfo
}

type Mapper struct {
	dir      string
	fileName string

	volumeLock    sync.Mutex
	sequenceLock  sync.Mutex
	Machines      []*Machine
	vid2machineId map[uint32]int //machineId is +1 of the index of []*Machine, to detect not found entries
	Writers       []uint32       // transient array of Writers volume id

	FileIdSequence uint64
	fileIdCounter  uint64

	volumeSizeLimit uint64
}

func NewMachine(server, publicUrl string, volumes []storage.VolumeInfo) *Machine {
	return &Machine{Server: MachineInfo{Url: server, PublicUrl: publicUrl}, Volumes: volumes}
}

func NewMapper(dirname string, filename string, volumeSizeLimit uint64) (m *Mapper) {
	m = &Mapper{dir: dirname, fileName: filename}
	m.vid2machineId = make(map[uint32]int)
	m.volumeSizeLimit = volumeSizeLimit
	m.Writers = *new([]uint32)
	m.Machines = *new([]*Machine)

	seqFile, se := os.OpenFile(path.Join(m.dir, m.fileName+".seq"), os.O_RDONLY, 0644)
	if se != nil {
		m.FileIdSequence = FileIdSaveInterval
		log.Println("Setting file id sequence", m.FileIdSequence)
	} else {
		decoder := gob.NewDecoder(seqFile)
		defer seqFile.Close()
		decoder.Decode(&m.FileIdSequence)
		log.Println("Loading file id sequence", m.FileIdSequence, "=>", m.FileIdSequence+FileIdSaveInterval)
		//in case the server stops between intervals
		m.FileIdSequence += FileIdSaveInterval
	}
	return
}
func (m *Mapper) PickForWrite(c string) (string, int, MachineInfo, error) {
	len_writers := len(m.Writers)
	if len_writers <= 0 {
		log.Println("No more writable volumes!")
		return "", 0, m.Machines[rand.Intn(len(m.Machines))].Server, errors.New("No more writable volumes!")
	}
	vid := m.Writers[rand.Intn(len_writers)]
	machine_id := m.vid2machineId[vid]
	if machine_id > 0 {
		machine := m.Machines[machine_id-1]
		fileId, count := m.NextFileId(c)
		if count == 0 {
			return "", 0, m.Machines[rand.Intn(len(m.Machines))].Server, errors.New("Strange count:" + c)
		}
		return NewFileId(vid, fileId, rand.Uint32()).String(), count, machine.Server, nil
	}
	return "", 0, m.Machines[rand.Intn(len(m.Machines))].Server, errors.New("Strangely vid " + strconv.FormatUint(uint64(vid), 10) + " is on no machine!")
}
func (m *Mapper) NextFileId(c string) (uint64, int) {
	count, parseError := strconv.ParseUint(c, 10, 64)
	if parseError != nil {
		if len(c) > 0 {
			return 0, 0
		}
		count = 1
	}
	m.sequenceLock.Lock()
	defer m.sequenceLock.Unlock()
	if m.fileIdCounter < count {
		m.fileIdCounter = FileIdSaveInterval
		m.FileIdSequence += FileIdSaveInterval
		m.saveSequence()
	}
	m.fileIdCounter = m.fileIdCounter - count
	return m.FileIdSequence - m.fileIdCounter, int(count)
}
func (m *Mapper) Get(vid uint32) (*Machine, error) {
	machineId := m.vid2machineId[vid]
	if machineId <= 0 {
		return nil, errors.New("invalid volume id " + strconv.FormatUint(uint64(vid), 10))
	}
	return m.Machines[machineId-1], nil
}
func (m *Mapper) Add(machine Machine) {
	//check existing machine, linearly
	//log.Println("Adding machine", machine.Server.Url)
	m.volumeLock.Lock()
	foundExistingMachineId := -1
	for index, entry := range m.Machines {
		if machine.Server.Url == entry.Server.Url {
			foundExistingMachineId = index
			break
		}
	}
	machineId := foundExistingMachineId
	if machineId < 0 {
		machineId = len(m.Machines)
		m.Machines = append(m.Machines, &machine)
	} else {
		m.Machines[machineId] = &machine
	}
	m.volumeLock.Unlock()

	//add to vid2machineId map, and Writers array
	for _, v := range machine.Volumes {
		m.vid2machineId[v.Id] = machineId + 1 //use base 1 indexed, to detect not found cases
	}
	//setting Writers, copy-on-write because of possible updating, this needs some future work!
	var writers []uint32
	for _, machine_entry := range m.Machines {
		for _, v := range machine_entry.Volumes {
			if uint64(v.Size) < m.volumeSizeLimit {
				writers = append(writers, v.Id)
			}
		}
	}
	m.Writers = writers
}
func (m *Mapper) saveSequence() {
	log.Println("Saving file id sequence", m.FileIdSequence, "to", path.Join(m.dir, m.fileName+".seq"))
	seqFile, e := os.OpenFile(path.Join(m.dir, m.fileName+".seq"), os.O_CREATE|os.O_WRONLY, 0644)
	if e != nil {
		log.Fatalf("Sequence File Save [ERROR] %s\n", e)
	}
	defer seqFile.Close()
	encoder := gob.NewEncoder(seqFile)
	encoder.Encode(m.FileIdSequence)
}
