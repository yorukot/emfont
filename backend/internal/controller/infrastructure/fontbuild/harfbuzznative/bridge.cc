//go:build cgo

#include "bridge.h"

#include <cstdlib>
#include <cstring>

#include <hb.h>
#include <hb-subset.h>
#include <woff2/encode.h>

namespace {

int fail(char **error_message, const char *message) {
    if (error_message != nullptr) {
        *error_message = ::strdup(message);
    }
    return 0;
}

}  // namespace

extern "C" int emfont_subset_woff2(
    const uint8_t *source,
    size_t source_length,
    const uint32_t *codepoints,
    size_t codepoint_count,
    size_t max_output_bytes,
    uint8_t **output,
    size_t *output_length,
    size_t *glyph_count,
    char **error_message
) {
    if (output != nullptr) *output = nullptr;
    if (output_length != nullptr) *output_length = 0;
    if (glyph_count != nullptr) *glyph_count = 0;
    if (error_message != nullptr) *error_message = nullptr;

    if (source == nullptr || source_length == 0 || codepoints == nullptr || codepoint_count == 0 ||
        max_output_bytes == 0 ||
        output == nullptr || output_length == nullptr || glyph_count == nullptr) {
        return fail(error_message, "invalid HarfBuzz subset input");
    }

    hb_blob_t *source_blob = hb_blob_create(
        reinterpret_cast<const char *>(source),
        static_cast<unsigned int>(source_length),
        HB_MEMORY_MODE_READONLY,
        nullptr,
        nullptr
    );
    if (source_blob == nullptr || hb_blob_get_length(source_blob) == 0) {
        if (source_blob != nullptr) hb_blob_destroy(source_blob);
        return fail(error_message, "HarfBuzz could not read the source font");
    }

    hb_face_t *source_face = hb_face_create(source_blob, 0);
    hb_blob_destroy(source_blob);
    if (source_face == nullptr || hb_face_get_glyph_count(source_face) == 0) {
        if (source_face != nullptr) hb_face_destroy(source_face);
        return fail(error_message, "HarfBuzz could not create a source font face");
    }

    hb_subset_input_t *subset_input = hb_subset_input_create_or_fail();
    hb_set_t *supported_unicodes = hb_set_create();
    hb_set_t *supported_variation_selectors = hb_set_create();
    if (subset_input == nullptr || supported_unicodes == nullptr || supported_variation_selectors == nullptr) {
        if (supported_variation_selectors != nullptr) hb_set_destroy(supported_variation_selectors);
        if (supported_unicodes != nullptr) hb_set_destroy(supported_unicodes);
        if (subset_input != nullptr) hb_subset_input_destroy(subset_input);
        hb_face_destroy(source_face);
        return fail(error_message, "HarfBuzz could not allocate subset state");
    }

    hb_face_collect_unicodes(source_face, supported_unicodes);
    hb_face_collect_variation_selectors(source_face, supported_variation_selectors);
    hb_set_t *requested_unicodes = hb_subset_input_unicode_set(subset_input);
    hb_set_clear(requested_unicodes);
    size_t matched = 0;
    for (size_t index = 0; index < codepoint_count; ++index) {
        const hb_codepoint_t codepoint = static_cast<hb_codepoint_t>(codepoints[index]);
        if (hb_set_has(supported_unicodes, codepoint) || hb_set_has(supported_variation_selectors, codepoint)) {
            hb_set_add(requested_unicodes, codepoint);
            ++matched;
        }
    }
    hb_set_destroy(supported_variation_selectors);
    hb_set_destroy(supported_unicodes);

    if (matched != codepoint_count) {
        hb_subset_input_destroy(subset_input);
        hb_face_destroy(source_face);
        fail(error_message, "one or more requested codepoints do not exist in the source font");
        return -1;
    }

    hb_face_t *subset_face = hb_subset_or_fail(source_face, subset_input);
    hb_subset_input_destroy(subset_input);
    hb_face_destroy(source_face);
    const unsigned int subset_glyph_count = subset_face == nullptr ? 0 : hb_face_get_glyph_count(subset_face);
    if (subset_face == nullptr || subset_glyph_count == 0) {
        if (subset_face != nullptr) hb_face_destroy(subset_face);
        return fail(error_message, "HarfBuzz failed to subset the source font");
    }

    hb_blob_t *subset_blob = hb_face_reference_blob(subset_face);
    hb_face_destroy(subset_face);
    unsigned int subset_length = 0;
    const char *subset_data = hb_blob_get_data(subset_blob, &subset_length);
    if (subset_data == nullptr || subset_length == 0) {
        hb_blob_destroy(subset_blob);
        return fail(error_message, "HarfBuzz produced an empty subset");
    }

    const size_t max_output_length = woff2::MaxWOFF2CompressedSize(
        reinterpret_cast<const uint8_t *>(subset_data), subset_length
    );
    if (max_output_length == 0) {
        hb_blob_destroy(subset_blob);
        return fail(error_message, "WOFF2 encoder rejected the subset font");
    }
    if (max_output_length > max_output_bytes) {
        hb_blob_destroy(subset_blob);
        fail(error_message, "WOFF2 output buffer exceeds the configured limit");
        return -2;
    }

    // The encoder includes alignment padding in compressed_length but does not
    // guarantee that every padding byte is written.
    uint8_t *compressed = static_cast<uint8_t *>(std::calloc(max_output_length, 1));
    if (compressed == nullptr) {
        hb_blob_destroy(subset_blob);
        return fail(error_message, "could not allocate WOFF2 output buffer");
    }

    size_t compressed_length = max_output_length;
    const bool converted = woff2::ConvertTTFToWOFF2(
        reinterpret_cast<const uint8_t *>(subset_data),
        subset_length,
        compressed,
        &compressed_length
    );
    hb_blob_destroy(subset_blob);
    if (!converted || compressed_length == 0) {
        std::free(compressed);
        return fail(error_message, "WOFF2 encoder failed to compress the subset font");
    }

    *output = compressed;
    *output_length = compressed_length;
    *glyph_count = subset_glyph_count;
    return 1;
}

extern "C" const char *emfont_harfbuzz_version(void) {
    return hb_version_string();
}

extern "C" void emfont_free(void *pointer) {
    std::free(pointer);
}
