// Cameras through PipeWire, as libcamera gives them: a laptop's MIPI camera (IPU6/IPU7) is not a
// V4L2 device Firefox can open directly.
pref("media.webrtc.camera.allow-pipewire", true);
