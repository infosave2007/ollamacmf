package cmfrunner

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CMF v2 container constants. See docs/CMF_V2_SPEC.md in the CMF repository.
const (
	cmfMagic           = "CMF\x01"
	cmfEnvelopeLength  = 128
	cmfVersion         = 2
	cmfMaxHeaderLength = 16 << 20

	// Feature bits the pinned Cortiq release understands. A file that
	// requires an unknown bit cannot be loaded by the runtime.
	cmfSupportedFeatures = 0x3fe7
	cmfSkillFileFeature  = 1 << 6
	cmfDecisionFeature   = 1 << 11

	cmfDirectoryHeaderLength = 16
	cmfDirectoryRecordLength = 56
)

// ErrInvalidCMF marks a file that is not a CMF v2 container this fork can run.
var ErrInvalidCMF = errors.New("invalid CMF file")

// Envelope is the fixed 128-byte CMF v2 envelope.
type Envelope struct {
	Version          uint32
	Flags            uint32
	RequiredFeatures uint32
	HeaderOffset     uint64
	HeaderLength     uint64
	DirectoryOffset  uint64
	DirectoryLength  uint64
	DataOffset       uint64
	DataLength       uint64
}

// Header is the subset of the CMF header JSON that ollamacmf uses to describe
// a model. Unknown fields are ignored, as the format specification requires.
type Header struct {
	Format string `json:"format"`
	Arch   struct {
		Name                  string `json:"arch_name"`
		HiddenSize            int    `json:"hidden_size"`
		NumLayers             int    `json:"num_layers"`
		VocabSize             int    `json:"vocab_size"`
		MaxPositionEmbeddings int    `json:"max_position_embeddings"`
	} `json:"arch"`
	QuantType  string `json:"quant_type"`
	Provenance struct {
		Tool        string `json:"tool"`
		SourceModel string `json:"source_model"`
		WeightQuant string `json:"weight_quant"`
		Pipeline    string `json:"pipeline"`
	} `json:"provenance"`
	TokenizerConfig struct {
		ChatTemplate string `json:"chat_template"`
	} `json:"tokenizer_config"`
}

// Info is everything ollamacmf learns about a CMF file without running it.
type Info struct {
	Envelope       Envelope
	Header         Header
	FileSize       int64
	ParameterCount uint64
}

// Architecture returns the model family name, for example "granite".
func (i *Info) Architecture() string {
	return strings.TrimSpace(i.Header.Arch.Name)
}

// ContextLength returns the context window declared by the model, or zero.
func (i *Info) ContextLength() int {
	return max(i.Header.Arch.MaxPositionEmbeddings, 0)
}

// Quantization returns a short quantization label such as "q4tp".
func (i *Info) Quantization() string {
	if q := strings.TrimSpace(i.Header.Provenance.WeightQuant); q != "" {
		return q
	}
	return strings.ToLower(strings.TrimSpace(i.Header.QuantType))
}

// SourceModel returns the Hugging Face model the file was converted from.
func (i *Info) SourceModel() string {
	return strings.TrimSpace(i.Header.Provenance.SourceModel)
}

// HasChatTemplate reports whether the model carries its own chat template.
func (i *Info) HasChatTemplate() bool {
	return strings.TrimSpace(i.Header.TokenizerConfig.ChatTemplate) != ""
}

// SupportsTools reports whether the chat template has a tools branch, which
// is the same signal Cortiq uses to advertise tool calling.
func (i *Info) SupportsTools() bool {
	template := i.Header.TokenizerConfig.ChatTemplate
	return strings.Contains(template, "tools") || strings.Contains(template, "tool_call")
}

// SupportsThinking reports whether the chat template has a switchable
// reasoning mode, which Cortiq drives through enable_thinking.
func (i *Info) SupportsThinking() bool {
	template := i.Header.TokenizerConfig.ChatTemplate
	return strings.Contains(template, "enable_thinking") ||
		strings.Contains(template, "<think>") ||
		strings.Contains(template, "reasoning_content") ||
		strings.Contains(template, "reasoning_effort")
}

// IsSkill reports whether the file is a standalone skill overlay.
func (i *Info) IsSkill() bool {
	return i.Envelope.RequiredFeatures&cmfSkillFileFeature != 0
}

// IsDecision reports whether the file is a decision (classifier) model.
func (i *Info) IsDecision() bool {
	return i.Envelope.RequiredFeatures&cmfDecisionFeature != 0 || strings.HasPrefix(i.Architecture(), "cortiq-decision")
}

// Kind classifies the model for user-facing messages: "text", "image",
// "video", "audio", "embedding", "decision", "skill" or "unknown".
func (i *Info) Kind() string {
	switch {
	case i.IsSkill():
		return "skill"
	case i.IsDecision():
		return "decision"
	}
	arch := strings.ToLower(i.Architecture())
	pipeline := strings.ToLower(i.Header.Provenance.Pipeline)
	for _, probe := range []string{arch, pipeline} {
		switch {
		case strings.Contains(probe, "whisper"):
			return "audio"
		case strings.Contains(probe, "video"), strings.Contains(probe, "ltx"):
			return "video"
		case strings.Contains(probe, "image"), strings.Contains(probe, "lumina"), strings.Contains(probe, "diffusion"):
			return "image"
		case strings.Contains(probe, "music"), strings.Contains(probe, "tts"):
			return "audio"
		case strings.Contains(probe, "embedding"):
			return "embedding"
		}
	}
	if i.Header.Arch.VocabSize == 0 || i.Header.Arch.NumLayers == 0 {
		return "unknown"
	}
	return "text"
}

// Runnable returns nil when the file is a generative text model ollamacmf can
// serve, or a descriptive error otherwise.
func (i *Info) Runnable() error {
	switch kind := i.Kind(); kind {
	case "text":
		return nil
	case "skill":
		return fmt.Errorf("%w: this file is a CMF skill overlay, not a runnable model; attach it to its base model with cortiq skill apply", ErrInvalidCMF)
	case "decision":
		return fmt.Errorf("%w: this is a CMF decision model, which the chat runner cannot serve", ErrInvalidCMF)
	case "unknown":
		return fmt.Errorf("%w: the header does not describe a text generation model", ErrInvalidCMF)
	default:
		return fmt.Errorf("%w: this is a CMF %s model; ollamacmf runs text generation models only", ErrInvalidCMF, kind)
	}
}

// ReadInfoFile opens path and reads its envelope, header and tensor directory.
func ReadInfoFile(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return ReadInfo(f, info.Size())
}

// ReadInfo reads the CMF metadata from r, which must be the whole file.
func ReadInfo(r io.ReaderAt, size int64) (*Info, error) {
	envelope, err := ReadEnvelope(r, size)
	if err != nil {
		return nil, err
	}
	info := &Info{Envelope: *envelope, FileSize: size}

	if envelope.HeaderLength > cmfMaxHeaderLength {
		return nil, fmt.Errorf("%w: header is %d bytes", ErrInvalidCMF, envelope.HeaderLength)
	}
	headerBytes := make([]byte, envelope.HeaderLength)
	if _, err := r.ReadAt(headerBytes, int64(envelope.HeaderOffset)); err != nil {
		return nil, fmt.Errorf("%w: read header: %v", ErrInvalidCMF, err)
	}
	if err := json.Unmarshal(headerBytes, &info.Header); err != nil {
		return nil, fmt.Errorf("%w: header is not valid JSON: %v", ErrInvalidCMF, err)
	}
	if info.Header.Format != "" && info.Header.Format != "cmf" {
		return nil, fmt.Errorf("%w: header format is %q", ErrInvalidCMF, info.Header.Format)
	}

	info.ParameterCount = countParameters(r, envelope)
	return info, nil
}

// ReadEnvelope validates the fixed envelope and section bounds of a CMF file.
func ReadEnvelope(r io.ReaderAt, size int64) (*Envelope, error) {
	if size < cmfEnvelopeLength {
		return nil, fmt.Errorf("%w: file is shorter than the %d-byte envelope", ErrInvalidCMF, cmfEnvelopeLength)
	}
	raw := make([]byte, cmfEnvelopeLength)
	if _, err := r.ReadAt(raw, 0); err != nil {
		return nil, fmt.Errorf("%w: read envelope: %v", ErrInvalidCMF, err)
	}
	return ParseEnvelope(raw, size)
}

// ParseEnvelope validates a 128-byte envelope against the file size.
func ParseEnvelope(raw []byte, size int64) (*Envelope, error) {
	if len(raw) < cmfEnvelopeLength || size < cmfEnvelopeLength {
		return nil, fmt.Errorf("%w: file is shorter than the %d-byte envelope", ErrInvalidCMF, cmfEnvelopeLength)
	}
	if !bytes.Equal(raw[:len(cmfMagic)], []byte(cmfMagic)) {
		return nil, fmt.Errorf("%w: expected a CMF v2 container", ErrInvalidCMF)
	}
	e := &Envelope{
		Version:          binary.LittleEndian.Uint32(raw[4:8]),
		Flags:            binary.LittleEndian.Uint32(raw[8:12]),
		RequiredFeatures: binary.LittleEndian.Uint32(raw[12:16]),
		HeaderOffset:     binary.LittleEndian.Uint64(raw[0x10:0x18]),
		HeaderLength:     binary.LittleEndian.Uint64(raw[0x18:0x20]),
		DirectoryOffset:  binary.LittleEndian.Uint64(raw[0x20:0x28]),
		DirectoryLength:  binary.LittleEndian.Uint64(raw[0x28:0x30]),
		DataOffset:       binary.LittleEndian.Uint64(raw[0x30:0x38]),
		DataLength:       binary.LittleEndian.Uint64(raw[0x38:0x40]),
	}
	if e.Version != cmfVersion {
		return nil, fmt.Errorf("%w: unsupported CMF version %d", ErrInvalidCMF, e.Version)
	}
	if e.Flags != 0 {
		return nil, fmt.Errorf("%w: unsupported envelope flags 0x%x", ErrInvalidCMF, e.Flags)
	}
	if unknown := e.RequiredFeatures &^ cmfSupportedFeatures; unknown != 0 {
		return nil, fmt.Errorf("%w: the file requires feature bits 0x%x that the pinned Cortiq release does not support", ErrInvalidCMF, unknown)
	}

	type section struct {
		name     string
		offset   int
		required bool
		aligned  bool
	}
	length := uint64(size)
	for _, s := range []section{
		{name: "header", offset: 0x10, required: true},
		{name: "directory", offset: 0x20, required: true},
		{name: "data", offset: 0x30, aligned: true},
		{name: "masks", offset: 0x40},
		{name: "vocabulary", offset: 0x50},
		{name: "index", offset: 0x60},
	} {
		off := binary.LittleEndian.Uint64(raw[s.offset : s.offset+8])
		n := binary.LittleEndian.Uint64(raw[s.offset+8 : s.offset+16])
		if s.required && n == 0 {
			return nil, fmt.Errorf("%w: required %s section is empty", ErrInvalidCMF, s.name)
		}
		if n == 0 {
			continue
		}
		if off > length || n > length-off {
			return nil, fmt.Errorf("%w: %s section is outside the file", ErrInvalidCMF, s.name)
		}
		if s.aligned && off%4096 != 0 {
			return nil, fmt.Errorf("%w: data section is not 4096-byte aligned", ErrInvalidCMF)
		}
	}
	return e, nil
}

// countParameters sums the element counts of every tensor in the directory.
// It is best effort: a malformed directory yields zero rather than an error,
// because Cortiq validates the directory itself when it loads the model.
func countParameters(r io.ReaderAt, e *Envelope) uint64 {
	if e.DirectoryLength < cmfDirectoryHeaderLength || e.DirectoryLength > 256<<20 {
		return 0
	}
	head := make([]byte, cmfDirectoryHeaderLength)
	if _, err := r.ReadAt(head, int64(e.DirectoryOffset)); err != nil {
		return 0
	}
	count := binary.LittleEndian.Uint64(head[0:8])
	records := uint64(cmfDirectoryRecordLength) * count
	if count == 0 || records > e.DirectoryLength-cmfDirectoryHeaderLength {
		return 0
	}
	raw := make([]byte, records)
	if _, err := r.ReadAt(raw, int64(e.DirectoryOffset+cmfDirectoryHeaderLength)); err != nil {
		return 0
	}
	var total uint64
	for i := uint64(0); i < count; i++ {
		rec := raw[i*cmfDirectoryRecordLength : (i+1)*cmfDirectoryRecordLength]
		ndim := int(rec[7])
		if ndim == 0 || ndim > 6 {
			continue
		}
		n := uint64(1)
		for d := range ndim {
			n *= uint64(binary.LittleEndian.Uint32(rec[8+4*d : 12+4*d]))
		}
		total += n
	}
	return total
}
