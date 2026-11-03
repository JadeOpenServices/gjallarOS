/* Prints what libusb tells an application about the QEMU USB stick, and
 * whether the device has an active configuration at that point. adb skips a
 * device that has none (FindInterface in client/usb_libusb.cpp). */
#include <libusb.h>
#include <stdio.h>

static int event(libusb_context *ctx, libusb_device *dev,
		 libusb_hotplug_event ev, void *data)
{
	struct libusb_device_descriptor d;
	struct libusb_config_descriptor *c;
	int r;

	(void)ctx;
	(void)data;
	libusb_get_device_descriptor(dev, &d);
	if (ev == LIBUSB_HOTPLUG_EVENT_DEVICE_ARRIVED) {
		r = libusb_get_active_config_descriptor(dev, &c);
		if (r == 0)
			libusb_free_config_descriptor(c);
		printf("arrived %04x:%04x config %s\n", d.idVendor, d.idProduct,
		       r == 0 ? "ok" : "missing");
	} else {
		printf("left %04x:%04x\n", d.idVendor, d.idProduct);
	}
	fflush(stdout);
	return 0;
}

int main(void)
{
	libusb_context *ctx;

	if (libusb_init(&ctx) != 0)
		return 1;
	if (!libusb_has_capability(LIBUSB_CAP_HAS_HOTPLUG))
		return 2;
	if (libusb_hotplug_register_callback(ctx,
			LIBUSB_HOTPLUG_EVENT_DEVICE_ARRIVED | LIBUSB_HOTPLUG_EVENT_DEVICE_LEFT,
			LIBUSB_HOTPLUG_ENUMERATE, 0x46f4, LIBUSB_HOTPLUG_MATCH_ANY,
			LIBUSB_HOTPLUG_MATCH_ANY, event, NULL, NULL) != 0)
		return 3;
	printf("listening\n");
	fflush(stdout);
	for (;;)
		libusb_handle_events(ctx);
}
