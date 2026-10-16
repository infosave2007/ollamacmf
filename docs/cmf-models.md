---
title: CMF model catalog
sidebarTitle: Model catalog
---

CMF models are published on Hugging Face under [huggingface.co/infosave](https://huggingface.co/infosave). This page lists the text generation repositories `ollamacmf` can run, with the pull command for each file. Sizes are the download sizes of the `.cmf` files.

The list below was taken on 16 October 2026. For the live list run:

```sh
ollamacmf cmf search
```

## How to read a pull command

`hf.co/<account>/<repository>:<file>` names one `.cmf` file in a repository. The part after the colon is the file name without `.cmf`; a unique fragment of it is accepted too, so `hf.co/infosave/Granite-4.2-cmf:8b-q4tp` is the same as the full name. Repositories with a single file need no tag.

Quantization suffixes: `q8_2f` is close to the original weights and roughly 1 byte per parameter; `q4tp` and `q4t` are about half that size with a small quality loss; `q2tp`, `q1t` and `q1` are the smallest and are meant for models that were trained or tuned for low bit widths.

## Small models (under 2 GB)

Good for laptops and quick experiments.

| Model | Pull | Size |
| --- | --- | --- |
| LFM2.5 230M | `ollamacmf pull hf.co/infosave/LFM2.5-cmf:lfm2.5-230m-q4tp` | 126 MB |
| Bonsai 1.7B (1-bit) | `ollamacmf pull hf.co/infosave/Bonsai-1.7Bcmf` | 319 MB |
| Hy-MT2 1.8B translation | `ollamacmf pull hf.co/infosave/Hy-MT2-cmf:hy-mt2-1.8b-q4tp` | 907 MB |
| MiniCPM-V 4.7 1B (text only here) | `ollamacmf pull hf.co/infosave/MiniCPM-V-4.7-1B-cmf:MiniCPM-V-4.7-1B-q4tp` | 761 MB |
| LFM2.5 2.6B | `ollamacmf pull hf.co/infosave/LFM2.5-cmf:lfm2.5-2.6b-q4tp` | 1.3 GB |
| Spark X2.5 1.7B | `ollamacmf pull hf.co/infosave/Spark-X2.5-cmf:Spark-X2.5-1.7B-q4mix` | 1.3 GB |
| MiniCPM5 2B | `ollamacmf pull hf.co/infosave/MiniCPM5-2B-cmf:MiniCPM5-2B-q4tp` | 1.5 GB |

## Medium models (2 to 8 GB)

| Model | Pull | Size |
| --- | --- | --- |
| Granite 4.2 3B | `ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-3b-q4tp` | 2.0 GB |
| Nanbeige 4.2 3B | `ollamacmf pull hf.co/infosave/Nanbeige4.2-3Bcmf` | 2.2 GB |
| Bonsai 8B (2-bit) | `ollamacmf pull hf.co/infosave/Bonsai-8B_2bit_cmf` | 2.2 GB |
| Spark X2.5 4B | `ollamacmf pull hf.co/infosave/Spark-X2.5-cmf:Spark-X2.5-4B-q4mix` | 3.0 GB |
| Hy-MT2 7B translation | `ollamacmf pull hf.co/infosave/Hy-MT2-cmf:hy-mt2-7b-q4tp` | 3.7 GB |
| LFM2.5 8B-A1B (MoE) | `ollamacmf pull hf.co/infosave/LFM2.5-cmf:lfm2.5-8b-a1b-q4tp` | 4.1 GB |
| Ornith 1.5 9B | `ollamacmf pull hf.co/infosave/Ornith-1.5-cmf:ornith-1.5-9b-q4tp` | 4.4 GB |
| Granite 4.2 8B | `ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-8b-q4tp` | 4.6 GB |
| Bonsai 27B (1-bit) | `ollamacmf pull hf.co/infosave/Bonsai-27Bcmf` | 4.8 GB |
| Mellum 2.1 12B-A2.5B Thinking (code) | `ollamacmf pull hf.co/infosave/Mellum2.1-12B-A2.5B-Thinking-CMF:mellum2.1-12b-a2.5b-thinking-q4tp` | 6.4 GB |
| humanizer 12B | `ollamacmf pull hf.co/infosave/humanizer-cmf:humanizer-12b-q4tp-ffn` | 7.4 GB |
| Ternary Bonsai 2 27B | `ollamacmf pull hf.co/infosave/Ternary-Bonsai-2-27B-cmf` | 7.7 GB |

## Large models (8 GB and above)

These need a machine with enough RAM or unified memory to hold the file.

| Model | Pull | Size |
| --- | --- | --- |
| KAT Coder V2.5 | `ollamacmf pull hf.co/infosave/KAT-Coder-V2.5-CMF` | 11.8 GB |
| Qwen3.6 35B-A3B Escha (2-bit) | `ollamacmf pull hf.co/infosave/Qwen3.6-35B-A3B-Escha-W2cmf` | 12.0 GB |
| Qwen3.6 27B | `ollamacmf pull hf.co/infosave/Qwen3.6-27Bcmf` | 13.3 GB |
| Qwen3.8 27B | `ollamacmf pull hf.co/infosave/Qwen3.8-27B-cmf:qwen38-27b-q4tp` | 13.3 GB |
| Granite 4.2 30B | `ollamacmf pull hf.co/infosave/Granite-4.2-cmf:granite-4.2-30b-q4tp` | 14.6 GB |
| Hy-MT2 30B-A3B translation | `ollamacmf pull hf.co/infosave/Hy-MT2-cmf:hy-mt2-30b-a3b-q4tp` | 14.7 GB |
| Kimi Linear 48B-A3B Code | `ollamacmf pull hf.co/infosave/Kimi-Linear-48B-A3B-Code-CMF` | 16.5 GB |
| Qwen3.6 35B-A3B | `ollamacmf pull hf.co/infosave/Qwen3.6-35B-A3Bcmf` | 17.4 GB |
| Ornith 1.5 35B-A3B | `ollamacmf pull hf.co/infosave/Ornith-1.5-cmf:ornith-1.5-35b-a3b-q4tp` | 17.4 GB |
| MiniCPM-V 4.7 35B-A3B (text only here) | `ollamacmf pull hf.co/infosave/MiniCPM-V-4.7-35B-A3B-cmf:MiniCPM-V-4.7-35B-A3B-q4tp` | 18.0 GB |
| Qwen3.8 Flash Next (2-bit) | `ollamacmf pull hf.co/infosave/Qwen3.8-Flash-Next-cmf:qwen38-flash-next-q2tp` | 71.7 GB |
| GLM 5.3 Flash (2-bit) | `ollamacmf pull hf.co/infosave/GLM-5.3-Flash-cmf:glm-5.3-flash-q2tp` | 108 GB |
| MiMo V2.6 Flash RL | `ollamacmf pull hf.co/infosave/MiMo-V2.6-Flash-RL-cmf:MiMo-V2.6-Flash-RL-q4tp` | 153 GB |

Most repositories also carry a `q8_2f` variant that is roughly twice the size of the `q4tp` one. Run `ollamacmf pull hf.co/infosave/<repository>` without a tag to see every file.

## Companion files

Some repositories contain files that go with a model rather than being one:

- `*.mtp.cmf`: a speculative-decoding draft head (`Qwen3.8-Flash-Next`, `MiMo-V2.6-Flash-RL`).
- `*.mm.cmf`: a multimodal projector.
- `*.skill.cmf`: a skill overlay for a base model.

`ollamacmf` skips them when choosing a default file and when matching tag fragments. They are not used by `ollamacmf` today; Cortiq's own CLI can apply them.

## Not supported by ollamacmf

These repositories hold CMF files for other Cortiq pipelines. `ollamacmf pull` refuses them with a message naming the model type; use the `cortiq` CLI for them.

| Repository | Type |
| --- | --- |
| `infosave/whisper-large-v3-turbo-cmf` | speech recognition |
| `infosave/EmbeddingGemma-2-cmf` | embeddings |
| `infosave/cmf-decision` | decision (classifier) model |
| `infosave/Z-Image-cmf`, `infosave/Z-Image-Turbo-cmf`, `infosave/Lumina-Image-2.0cmf`, `infosave/Qwen-Image-2.1-cmf`, `infosave/Qwen-Image-Edit-2509-cmf` | image generation and editing |
| `infosave/LTX-2.5-cmf`, `infosave/MiniMax-H3-Turbo-cmf` | video generation |
| `infosave/MiniMax-Music-3-cmf` | music generation |

## Converting your own model

Cortiq converts Hugging Face checkpoints and GGUF files into CMF:

```sh
cargo install cortiq-cli
cortiq convert --model Qwen/Qwen3-0.6B --quant q4tp --output qwen3-0.6b.cmf
ollamacmf run ./qwen3-0.6b.cmf
```

See the [Cortiq README](https://github.com/infosave2007/cmf#quick-start) for the supported model families and quantization options.
