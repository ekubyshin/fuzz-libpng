/*
 * Minimal AFL++ harness: input file (argv[1], AFL "@@") or stdin ->
 * png_image_begin_read_from_memory -> png_image_finish_read.
 */
#include <png.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MAX_INPUT (1U << 20)

static unsigned char buf[MAX_INPUT];

int main(int argc, char **argv) {
  size_t n = 0;
  if (argc > 1) {
    FILE *f = fopen(argv[1], "rb");
    if (f == NULL)
      return 0;
    n = fread(buf, 1, sizeof buf, f);
    fclose(f);
  } else {
    n = fread(buf, 1, sizeof buf, stdin);
  }
  if (n == 0)
    return 0;

  png_image image;
  memset(&image, 0, sizeof image);
  image.version = PNG_IMAGE_VERSION;

  if (!png_image_begin_read_from_memory(&image, buf, n))
    goto free_image;

  /* Request RGBA output; png_image_finish_read handles palette/interlace/etc. */
  image.format = PNG_FORMAT_RGBA;

  if (PNG_IMAGE_FAILED(image))
    goto free_image;

  {
    png_alloc_size_t sz = PNG_IMAGE_SIZE(image);
    void *out = malloc(sz);
    if (out == NULL)
      goto free_image;

    (void)png_image_finish_read(&image, NULL /* background */, out, 0, NULL);
    free(out);
  }

free_image:
  png_image_free(&image);
  return 0;
}
