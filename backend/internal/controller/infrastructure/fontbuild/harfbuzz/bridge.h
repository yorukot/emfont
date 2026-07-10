#ifndef EMFONT_HARFBUZZ_BRIDGE_H
#define EMFONT_HARFBUZZ_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

int emfont_subset_woff2(
    const uint8_t *source,
    size_t source_length,
    const uint32_t *codepoints,
    size_t codepoint_count,
    uint8_t **output,
    size_t *output_length,
    size_t *supported_codepoint_count,
    char **error_message
);

const char *emfont_harfbuzz_version(void);
void emfont_free(void *pointer);

#ifdef __cplusplus
}
#endif

#endif
