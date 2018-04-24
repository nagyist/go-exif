package exif

import (
    "errors"
    "fmt"
    "bytes"
    "strings"

    "encoding/binary"

    "github.com/dsoprea/go-logging"
)

var (
    ifdBuilderLogger = log.NewLogger("exif.ifd_builder")
)

var (
    ErrTagEntryNotFound = errors.New("tag entry not found")
)


type IfdBuilderTagValue struct {
    valueBytes []byte
    ib *IfdBuilder
}

func NewIfdBuilderTagValueFromBytes(valueBytes []byte) *IfdBuilderTagValue {
    return &IfdBuilderTagValue{
        valueBytes: valueBytes,
    }
}

func NewIfdBuilderTagValueFromIfdBuilder(ib *IfdBuilder) *IfdBuilderTagValue {
    return &IfdBuilderTagValue{
        ib: ib,
    }
}

func (ibtv IfdBuilderTagValue) IsBytes() bool {
    return ibtv.valueBytes != nil
}

func (ibtv IfdBuilderTagValue) Bytes() []byte {
    if ibtv.IsBytes() == false {
        log.Panicf("this tag is not a byte-slice value")
    }

    return ibtv.valueBytes
}

func (ibtv IfdBuilderTagValue) IsIb() bool {
    return ibtv.ib != nil
}

func (ibtv IfdBuilderTagValue) Ib() *IfdBuilder {
    if ibtv.IsIb() == false {
        log.Panicf("this tag is not an IFD-builder value")
    }

    return ibtv.ib
}


type builderTag struct {
    // ifdName is non-empty if represents a child-IFD.
    ifdName string

    tagId uint16

    // value is either a value that can be encoded, an IfdBuilder instance (for
    // child IFDs), or an IfdTagEntry instance representing an existing,
    // previously-stored tag.
    value *IfdBuilderTagValue
}

func (bt builderTag) String() string {
    valuePhrase := ""

    if bt.value.IsBytes() == true {
        valueBytes := bt.value.Bytes()

        if len(valueBytes) <= 8 {
            valuePhrase = fmt.Sprintf("%v", valueBytes)
        } else {
            valuePhrase = fmt.Sprintf("%v...", valueBytes[:8])
        }
    } else {
        valuePhrase = fmt.Sprintf("%v", bt.value.Ib())
    }

    return fmt.Sprintf("BuilderTag<TAG-ID=(0x%02x) IFD=[%s] VALUE=[%v]>", bt.tagId, bt.ifdName, valuePhrase)
}


type IfdBuilder struct {
    // ifdName is the name of the IFD that owns the current tag.
    ifdName string

    // ifdTagId will be non-zero if we're a child IFD.
    ifdTagId uint16

    byteOrder binary.ByteOrder

    // Includes both normal tags and IFD tags (which point to child IFDs).
    tags []builderTag

    // existingOffset will be the offset that this IFD is currently found at if
    // it represents an IFD that has previously been stored (or 0 if not).
    existingOffset uint32

    // nextIfd represents the next link if we're chaining to another.
    nextIfd *IfdBuilder
}

func NewIfdBuilder(ifdName string, byteOrder binary.ByteOrder) (ib *IfdBuilder) {
    ib = &IfdBuilder{
        ifdName: ifdName,

        // ifdName is empty unless it's a child-IFD.
        ifdTagId: IfdTagIds[ifdName],

        byteOrder: byteOrder,
        tags: make([]builderTag, 0),
    }

    return ib
}

// NewIfdBuilderWithExistingIfd creates a new IB using the same header type
// information as the given IFD.
func NewIfdBuilderWithExistingIfd(ifd *Ifd) (ib *IfdBuilder) {
    ifdTagId, found := IfdTagIds[ifd.Name]
    if found == false {
        log.Panicf("tag-ID for IFD not found: [%s]", ifd.Name)
    }

    ib = &IfdBuilder{
        ifdName: ifd.Name,
        ifdTagId: ifdTagId,
        byteOrder: ifd.ByteOrder,
        existingOffset: ifd.Offset,
    }

    return ib
}

// NewIfdBuilderFromExistingChain creates a chain of IB instances from an
// IFD chain generated from real data.
func NewIfdBuilderFromExistingChain(rootIfd *Ifd, exifData []byte) (rootIb *IfdBuilder) {
    itevr := NewIfdTagEntryValueResolver(exifData, rootIfd.ByteOrder)

// TODO(dustin): !! When we actually write the code to flatten the IB to bytes, make sure to skip the tags that have a nil value (which will happen when we add-from-exsting without a resolver instance).

    var newIb *IfdBuilder
    for thisExistingIfd := rootIfd; thisExistingIfd != nil; thisExistingIfd = thisExistingIfd.NextIfd {
        lastIb := newIb

        ifdName := thisExistingIfd.Name
        if ifdName == "" {
            ifdName = IfdStandard
        }

        newIb = NewIfdBuilder(ifdName, binary.BigEndian)
        if lastIb != nil {
            lastIb.SetNextIfd(newIb)
        }

        if rootIb == nil {
            rootIb = newIb
        }

        err := newIb.AddTagsFromExisting(thisExistingIfd, itevr, nil, nil)
        log.PanicIf(err)

        // Any child IFDs will still not be copied. Do that now.

        for _, childIfd := range thisExistingIfd.Children {
            childIb := NewIfdBuilderFromExistingChain(childIfd, exifData)

            err = newIb.AddChildIb(childIb)
            log.PanicIf(err)
        }
    }

    return rootIb
}

func (ib *IfdBuilder) String() string {
    nextIfdPhrase := ""
    if ib.nextIfd != nil {
        nextIfdPhrase = ib.nextIfd.ifdName
    }

    return fmt.Sprintf("IfdBuilder<NAME=[%s] TAG-ID=(0x%02x) BO=[%s] COUNT=(%d) OFFSET=(0x%04x) NEXT-IFD=(0x%04x)>", ib.ifdName, ib.ifdTagId, ib.byteOrder, len(ib.tags), ib.existingOffset, nextIfdPhrase)
}


// ifdOffsetIterator keeps track of where the next IFD should be written
// (relative to the end of the EXIF header bytes; all addresses are relative to
// this).
type ifdOffsetIterator struct {
    offset uint32
}

func (ioi *ifdOffsetIterator) Step(size uint32) {
    ioi.offset += size
}

func (ioi *ifdOffsetIterator) Offset() uint32 {
    return ioi.offset
}

func (ib *IfdBuilder) Tags() (tags []builderTag) {
    return ib.tags
}

func (ib *IfdBuilder) dump(levels int) {
    indent := strings.Repeat(" ", levels * 4)

    if levels == 0 {
        fmt.Printf("%sIFD: %s\n", indent, ib)
    } else {
        fmt.Printf("%sChild IFD: %s\n", indent, ib)
    }

    ti := NewTagIndex()

    if len(ib.tags) > 0 {
        fmt.Printf("\n")

        for i, tag := range ib.tags {
            _, isChildIb := IfdTagNames[tag.tagId]

            tagName := ""

            // If a normal tag (not a child IFD) get the name.
            if isChildIb == true {
                tagName = "<Child IFD>"
            } else {
                it, err := ti.Get(tag.ifdName, tag.tagId)
                if log.Is(err, ErrTagNotFound) == true {
                    tagName = "<UNKNOWN>"
                } else if err != nil {
                    log.Panic(err)
                } else {
                    tagName = it.Name
                }
            }

            fmt.Printf("%s  (%d): [%s] %s\n", indent, i, tagName, tag)

            if isChildIb == true {
                if tag.value.IsIb() == false {
                    log.Panicf("tag-ID (0x%02x) is an IFD but the tag value is not an IB instance: %v", tag.tagId, tag)
                }

                fmt.Printf("\n")

                childIb := tag.value.Ib()
                childIb.dump(levels + 1)
            }
        }

        fmt.Printf("\n")
    }
}

func (ib *IfdBuilder) Dump() {
    ib.dump(0)
}

func (ib *IfdBuilder) dumpToStrings(thisIb *IfdBuilder, prefix string, lines []string) (linesOutput []string) {
    if lines == nil {
        linesOutput = make([]string, 0)
    } else {
        linesOutput = lines
    }

    for i, tag := range thisIb.tags {
        line := fmt.Sprintf("<PARENTS=[%s] IFD-NAME=[%s]> IFD-TAG-ID=(0x%02x) CHILD-IFD=[%s] INDEX=(%d) TAG=[0x%02x]", prefix, thisIb.ifdName, thisIb.ifdTagId, tag.ifdName, i, tag.tagId)
        linesOutput = append(linesOutput, line)

        if tag.ifdName != "" {
            if tag.value.IsIb() == false {
                log.Panicf("tag has IFD tag-ID (0x%02x) but not a child IB instance: %v", tag.tagId, tag)
            }

            childPrefix := ""
            if prefix == "" {
                childPrefix = fmt.Sprintf("%s", thisIb.ifdName)
            } else {
                childPrefix = fmt.Sprintf("%s->%s", prefix, thisIb.ifdName)
            }

            linesOutput = thisIb.dumpToStrings(tag.value.Ib(), childPrefix, linesOutput)
        }
    }

    return linesOutput
}

func (ib *IfdBuilder) DumpToStrings() (lines []string) {
    return ib.dumpToStrings(ib, "", lines)
}

// // calculateRawTableSize returns the number of bytes required just to store the
// // basic IFD header and tags. This needs to be called before we can even write
// // the tags so that we can know where the data starts and can calculate offsets.
// func (ib *IfdBuilder) calculateTableSize() (size uint32, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()


// // TODO(dustin): !! Finish.


//     return 0, nil
// }

// // calculateDataSize returns the number of bytes required the offset-based data
// // of the IFD.
// func (ib *IfdBuilder) calculateDataSize(tableSize uint32) (size uint32, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()


// // TODO(dustin): !! Finish.


//     return 0, nil
// }

// // generateBytes populates the given table and data byte-arrays. `dataOffset`
// // is the distance from the beginning of the IFD to the beginning of the IFD's
// // data (following the IFD's table). It may be used to calculate the final
// // offset of the data we store there so that we can reference it from the IFD
// // table. The `ioi` is used to know where to insert child IFDs at.
// //
// // len(ifdTableRaw) == calculateTableSize()
// // len(ifdDataRaw) == calculateDataSize()
// func (ib *IfdBuilder) generateBytes(dataOffset uint32, ifdTableRaw, ifdDataRaw []byte, ioi *ifdOffsetIterator) (err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()


// // TODO(dustin): !! Finish.

// // TODO(dustin): !! Some offsets of existing IFDs will have to be reallocated if there are any updates. We'll need to be able to resolve the original value against the original EXIF data for that, which we currently don't have access to, yet, from here.
// // TODO(dustin): !! Test that the offsets are identical if there are no changes (on principle).


//     return nil
// }

// // allocateIfd will produce the two byte-arrays for every IFD and bump the IOI
// // for the next IFD. This is the foundation of how offsets are calculated.
// func (ib *IfdBuilder) allocateIfd(tableSize, dataSize uint32, ioi *ifdOffsetIterator) (tableRaw []byte, dataRaw []byte, dataOffset uint32, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()

//     // Allocate the size required and iterate our offset marker
//     // appropriately so the IFD-build knows where it can calculate its
//     // offsets from.

//     tableRaw = make([]byte, tableSize)
//     dataRaw = make([]byte, dataSize)

//     dataOffset = ioi.Offset() + tableSize
//     ioi.Step(tableSize + dataSize)

//     return tableRaw, dataRaw, dataOffset, nil
// }

// // BuildExif returns a new byte array of EXIF data.
// func (ib *IfdBuilder) BuildExif() (new []byte, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()

//     b := bytes.Buffer{}

//     ioi := &ifdOffsetIterator{
//         offset: RootIfdExifOffset,
//     }

//     ptr := ib

//     for ; ptr != nil ; {
//         // Figure out the size requirements.

//         tableSize, err := ptr.calculateTableSize()
//         log.PanicIf(err)

//         dataSize, err := ptr.calculateDataSize(tableSize)
//         log.PanicIf(err)

//         // Allocate the size required and iterate our offset marker
//         // appropriately so the IFD-build knows where it can calculate its
//         // offsets from.

//         tableRaw, dataRaw, dataOffset, err := ib.allocateIfd(tableSize, dataSize, ioi)
//         log.PanicIf(err)

//         // Build.

//         err = ptr.generateBytes(dataOffset, tableRaw, dataRaw, ioi)
//         log.PanicIf(err)

//         // Attach the new data to the stream.

//         _, err = b.Write(tableRaw)
//         log.PanicIf(err)

//         _, err = b.Write(dataRaw)
//         log.PanicIf(err)

//         ptr = ptr.nextIfd

//         // Write the offset of the next IFD (or 0x0 for none).

//         nextIfdOffset := uint32(0)

//         if ptr != nil {
//             // This might've been iterated by `generateBytes()`. It'll also
//             // point at the next offset that we can install an IFD to.
//             nextIfdOffset = ioi.Offset()
//         }

//         nextIfdOffsetBytes := make([]byte, 4)
//         ib.byteOrder.PutUint32(nextIfdOffsetBytes, nextIfdOffset)

//         _, err = b.Write(nextIfdOffsetBytes)
//         log.PanicIf(err)
//     }

//     return b.Bytes(), nil
// }

func (ib *IfdBuilder) SetNextIfd(nextIfd *IfdBuilder) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    ib.nextIfd = nextIfd

    return nil
}

func (ib *IfdBuilder) DeleteN(tagId uint16, n int) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    if n < 1 {
        log.Panicf("N must be at least 1: (%d)", n)
    }

    for ; n > 0; {
        j := -1
        for i, bt := range ib.tags {
            if bt.tagId == tagId {
                j = i
                break
            }
        }

        if j == -1 {
            log.Panic(ErrTagEntryNotFound)
        }

        ib.tags = append(ib.tags[:j], ib.tags[j + 1:]...)
        n--
    }

    return nil
}

func (ib *IfdBuilder) DeleteFirst(tagId uint16) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    err = ib.DeleteN(tagId, 1)
    log.PanicIf(err)

    return nil
}

func (ib *IfdBuilder) DeleteAll(tagId uint16) (n int, err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    for {
        err = ib.DeleteN(tagId, 1)
        if log.Is(err, ErrTagEntryNotFound) == true {
            break
        } else if err != nil {
            log.Panic(err)
        }

        n++
    }

    return n, nil
}

func (ib *IfdBuilder) ReplaceAt(position int, bt builderTag) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    if position < 0 {
        log.Panicf("replacement position must be 0 or greater")
    } else if position >= len(ib.tags) {
        log.Panicf("replacement position does not exist")
    }

    ib.tags[position] = bt

    return nil
}

func (ib *IfdBuilder) Replace(tagId uint16, bt builderTag) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    position, err := ib.Find(tagId)
    log.PanicIf(err)

    ib.tags[position] = bt

    return nil
}

func (ib *IfdBuilder) FindN(tagId uint16, maxFound int) (found []int, err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    found = make([]int, 0)

    for i, bt := range ib.tags {
        if bt.tagId == tagId {
            found = append(found, i)
            if maxFound == 0 || len(found) >= maxFound {
                break
            }
        }
    }

    return found, nil
}

func (ib *IfdBuilder) Find(tagId uint16) (position int, err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    found, err := ib.FindN(tagId, 1)
    log.PanicIf(err)

    if len(found) == 0 {
        log.Panic(ErrTagEntryNotFound)
    }

    return found[0], nil
}

// TODO(dustin): !! Switch to producing bytes immediately so that they're validated.

func (ib *IfdBuilder) Add(bt builderTag) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    if bt.value.IsIb() == true {
        log.Panicf("child IfdBuilders must be added via AddChildIb() not Add()")
    }

    ib.tags = append(ib.tags, bt)
    return nil
}

// AddChildIb adds a tag that branches to a new IFD.
func (ib *IfdBuilder) AddChildIb(childIb *IfdBuilder) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    if childIb.ifdTagId == 0 {
        log.Panicf("IFD can not be used as a child IFD (not associated with a tag-ID): %v", childIb)
    } else if childIb.byteOrder != ib.byteOrder {
        log.Panicf("Child IFD does not have the same byte-order: [%s] != [%s]", childIb.byteOrder, ib.byteOrder)
    }

    // Since no standard IFDs supports occuring more than once, check that a
    // tag of this type has not been previously added. Note that we just search
    // the current IFD and *not every* IFD.
    for _, bt := range childIb.tags {
        if bt.tagId == childIb.ifdTagId {
            log.Panicf("child-IFD already added: [%s]", childIb.ifdName)
        }
    }

    value := NewIfdBuilderTagValueFromIfdBuilder(childIb)

    bt := builderTag{
        ifdName: childIb.ifdName,
        tagId: childIb.ifdTagId,
        value: value,
    }

    ib.tags = append(ib.tags, bt)

    return nil
}

// AddTagsFromExisting does a verbatim copy of the entries in `ifd` to this
// builder. It excludes child IFDs. These must be added explicitly via
// `AddChildIb()`.
func (ib *IfdBuilder) AddTagsFromExisting(ifd *Ifd, itevr *IfdTagEntryValueResolver, includeTagIds []uint16, excludeTagIds []uint16) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    for _, ite := range ifd.Entries {
        // If we want to add an IFD tag, we'll have to build it first and *then*
        // add it via a different method.
        if ite.ChildIfdName != "" {
            continue
        }

        if excludeTagIds != nil && len(excludeTagIds) > 0 {
            found := false
            for _, excludedTagId := range excludeTagIds {
                if excludedTagId == ite.TagId {
                    found = true
                }
            }

            if found == true {
                continue
            }
        }

        if includeTagIds != nil && len(includeTagIds) > 0 {
            // Whether or not there was a list of excludes, if there is a list
            // of includes than the current tag has to be in it.

            found := false
            for _, includedTagId := range includeTagIds {
                if includedTagId == ite.TagId {
                    found = true
                    break
                }
            }

            if found == false {
                continue
            }
        }

        bt := builderTag{
            tagId: ite.TagId,
        }

        if itevr == nil {
            // rawValueOffsetCopy is our own private copy of the original data.
            // It should always be four-bytes, but just copy whatever there is.
            rawValueOffsetCopy := make([]byte, len(ite.RawValueOffset))
            copy(rawValueOffsetCopy, ite.RawValueOffset)

            bt.value = NewIfdBuilderTagValueFromBytes(rawValueOffsetCopy)
        } else {
            var err error

            valueBytes, err := itevr.ValueBytes(&ite)
            if err != nil {
                if log.Is(err, ErrUnhandledUnknownTypedTag) == true {
                    ifdBuilderLogger.Warningf(nil, "Unknown-type tag can't be parsed so it can't be copied to the new IFD.")
                    continue
                }

                log.Panic(err)
            }

            bt.value = NewIfdBuilderTagValueFromBytes(valueBytes)
        }

        err := ib.Add(bt)
        log.PanicIf(err)
    }

    return nil
}


type ByteWriter struct {
    b *bytes.Buffer
    byteOrder binary.ByteOrder
}

func NewByteWriter(b *bytes.Buffer, byteOrder binary.ByteOrder) (bw *ByteWriter) {
    return &ByteWriter{
        b: b,
        byteOrder: byteOrder,
    }
}

func (bw ByteWriter) WriteAsBytes(value interface{}) (err error) {
    defer func() {
        if state := recover(); state != nil {
            err = log.Wrap(state.(error))
        }
    }()

    err = binary.Write(bw.b, bw.byteOrder, value)
    log.PanicIf(err)

    return nil
}


type IfdByteEncoder struct {
}

func NewIfdByteEncoder() (ibe *IfdByteEncoder) {
    return new(IfdByteEncoder)
}

// // encodeToBytes encodes the given IB to a byte-slice. We are given the offset
// // at which this IFD will be written.
// func (ibe *IfdByteEncoder) encodeToBytes(ib *IfdBuilder, ifdAddressableOffset uint32) (data []byte, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()

//     b := new(bytes.Buffer)
//     bw := NewByteWriter(b, ib.byteOrder)

//     // Write tag count.
//     err = bw.WriteAsBytes(uint16(len(ib.tags)))
//     log.PancIf(err)

//     ti := NewTagIndex()

//     childIbs := make([]*IfdBuilder, 0)
//     byteCount := ifdAddressableOffset
//     for _, bt := range ib.tags {
//         err := bw.WriteAsBytes(uint16(bt.tagId))
//         log.PancIf(err)

//         it, err := ti.Get(ib.ifdName, bt.tagId)
//         log.PanicIf(err)

//         err = bw.WriteAsBytes(uint16(it.Type))
//         log.PancIf(err)

//     }

//     // for i := uint16(0); i < tagCount; i++ {
//     //     tagId, _, err := ite.getUint16()
//     //     log.PanicIf(err)

//     //     tagType, _, err := ite.getUint16()
//     //     log.PanicIf(err)

//     //     unitCount, _, err := ite.getUint32()
//     //     log.PanicIf(err)

//     //     valueOffset, rawValueOffset, err := ite.getUint32()
//     //     log.PanicIf(err)
//     // }

//     // nextIfdOffset, _, err = ite.getUint32()
//     // log.PanicIf(err)

//     // Write address of next IFD in chain.
// // TODO(dustin): !! Finish.
//     err = bw.WriteAsBytes(uint32(0))
//     log.PancIf(err)


// }

// func (ib *IfdBuilder) EncodeToBytes(ib *IfdBuilder) (data []byte, err error) {
//     defer func() {
//         if state := recover(); state != nil {
//             err = log.Wrap(state.(error))
//         }
//     }()

//     data, err = ib.encodeToBytes(ib, ExifAddressableAreaStart)
//     log.PanicIf(err)

//     return data, nil
// }