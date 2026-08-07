// nastro-tap: macOS system-audio + microphone capture helper for nastro.
//
// Writes an incrementally-encoded AAC/.m4a file so the recording survives a
// mid-capture kill (SIGINT/SIGTERM finalize the file cleanly; data already
// flushed to disk before that stays intact either way since we never buffer
// the whole recording in memory).
//
// Requires macOS 14.4+: the CoreAudio process-tap surface used for system-wide
// audio capture (CATapDescription, AudioHardwareCreateProcessTap, aggregate
// devices assembled with kAudioAggregateDeviceTapListKey) was introduced in
// 14.2/14.4 and has no earlier equivalent.
//
// CLI contract (depended on by the Go side, keep in sync):
//   nastro-tap <audio-output-path> [--mic-only|--system-only]
// Exit codes: 0 clean stop via signal, 2 missing audio-capture permission
// (TCC), 1 any other startup/usage failure.

import Foundation
import Dispatch
import CoreAudio
import AudioToolbox
import AVFoundation

// MARK: - Errors

/// `.permissionDenied` maps to exit code 2 (the Go side special-cases it to
/// show a friendlier "grant permission" message); `.general` maps to 1.
enum CaptureError: Error, CustomStringConvertible {
    case permissionDenied(String)
    case general(String)

    var description: String {
        switch self {
        case .permissionDenied(let message), .general(let message):
            return message
        }
    }
}

// MARK: - CoreAudio property helpers

/// Minimal, focused property readers -- just the three properties this tool
/// actually needs, rather than a general-purpose CoreAudio property framework.

private func readDefaultSystemOutputDevice() throws -> AudioDeviceID {
    var address = AudioObjectPropertyAddress(
        mSelector: kAudioHardwarePropertyDefaultSystemOutputDevice,
        mScope: kAudioObjectPropertyScopeGlobal,
        mElement: kAudioObjectPropertyElementMain)
    var deviceID = AudioDeviceID(kAudioObjectUnknown)
    var size = UInt32(MemoryLayout<AudioDeviceID>.size)
    let status = AudioObjectGetPropertyData(AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size, &deviceID)
    guard status == noErr else {
        throw CaptureError.general("Failed to read the default system output device: OSStatus \(status)")
    }
    return deviceID
}

private func readDeviceUID(_ deviceID: AudioDeviceID) throws -> String {
    var address = AudioObjectPropertyAddress(
        mSelector: kAudioDevicePropertyDeviceUID,
        mScope: kAudioObjectPropertyScopeGlobal,
        mElement: kAudioObjectPropertyElementMain)
    // CFString properties follow Core Audio's "get rule follows create rule":
    // the caller owns the returned reference, hence Unmanaged + takeRetainedValue.
    var uid: Unmanaged<CFString>?
    var size = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
    let status = withUnsafeMutablePointer(to: &uid) { pointer in
        AudioObjectGetPropertyData(deviceID, &address, 0, nil, &size, pointer)
    }
    guard status == noErr, let uid else {
        throw CaptureError.general("Failed to read output device UID: OSStatus \(status)")
    }
    return uid.takeRetainedValue() as String
}

private func readTapFormat(_ tapID: AudioObjectID) throws -> AudioStreamBasicDescription {
    var address = AudioObjectPropertyAddress(
        mSelector: kAudioTapPropertyFormat,
        mScope: kAudioObjectPropertyScopeGlobal,
        mElement: kAudioObjectPropertyElementMain)
    var description = AudioStreamBasicDescription()
    var size = UInt32(MemoryLayout<AudioStreamBasicDescription>.size)
    let status = AudioObjectGetPropertyData(tapID, &address, 0, nil, &size, &description)
    guard status == noErr else {
        throw CaptureError.general("Failed to read process tap stream format: OSStatus \(status)")
    }
    return description
}

// MARK: - Error classification

/// `kAudioDevicePermissionsError` ('!hog') is CoreAudio's permission-denied
/// status; the process-tap/aggregate-device APIs surface a missing "Screen &
/// System Audio Recording" TCC authorization this way.
private func classifyCoreAudioError(_ status: OSStatus, context: String) -> CaptureError {
    if status == kAudioDevicePermissionsError {
        return .permissionDenied(
            "System audio capture requires the \"Screen & System Audio Recording\" permission " +
            "for this terminal application. Grant it in System Settings > Privacy & Security > " +
            "Screen & System Audio Recording, then try again. macOS should also prompt for it directly.")
    }
    return .general("\(context): OSStatus \(status)")
}

/// `kAudioUnitErr_Unauthorized` is what AVAudioEngine's underlying audio unit
/// surfaces when the Microphone TCC permission is denied.
private func classifyMicError(_ error: Error) -> CaptureError {
    if AVAudioApplication.shared.recordPermission == .denied {
        return micPermissionError
    }
    let nsError = error as NSError
    if nsError.domain == NSOSStatusErrorDomain && nsError.code == -10847 {
        return micPermissionError
    }
    return .general("Failed to start audio engine: \(error.localizedDescription)")
}

private let micPermissionError = CaptureError.permissionDenied(
    "Microphone capture requires the \"Microphone\" permission for this terminal application. " +
    "Grant it in System Settings > Privacy & Security > Microphone, then try again. macOS should " +
    "also prompt for it directly.")

/// Must run before *any* touch of `AVAudioEngine.inputNode` (reading its format,
/// connecting it). When Microphone TCC is denied, the input node's underlying
/// audio unit can't actually adopt its own reported format, and
/// `AVAudioEngine.connect` -- which is not a throwing call -- raises an
/// uncaught NSException instead of returning a Swift error. Resolving
/// permission up front (requesting it if undetermined, since macOS won't
/// prompt on its own for a bare CLI binary) keeps that path from ever crashing.
private func ensureMicrophonePermission() throws {
    switch AVAudioApplication.shared.recordPermission {
    case .granted:
        return
    case .undetermined:
        // Blocking this thread with a semaphore would deadlock: the
        // completion handler is delivered via the main queue, which never
        // gets pumped until RunLoop.main.run() (at the bottom of this file)
        // starts. Pumping the run loop by hand here lets that callback fire
        // while we wait for it.
        var granted = false
        var responded = false
        AVAudioApplication.requestRecordPermission { result in
            granted = result
            responded = true
        }
        while !responded {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.05))
        }
        guard granted else { throw micPermissionError }
    case .denied:
        throw micPermissionError
    @unknown default:
        throw micPermissionError
    }
}

// MARK: - AAC settings

private func aacSettings(sampleRate: Double, channelCount: AVAudioChannelCount) -> [String: Any] {
    [
        AVFormatIDKey: kAudioFormatMPEG4AAC,
        AVSampleRateKey: sampleRate,
        AVNumberOfChannelsKey: Int(channelCount),
        AVEncoderAudioQualityKey: AVAudioQuality.high.rawValue,
    ]
}

// MARK: - Ring buffer

/// Bridges a push-driven capture callback (its own hardware clock/thread --
/// the system tap's IOProc, or an AVAudioEngine input tap) into the mixer's
/// pull-driven timer (see `SampleMixer`, used only in mixed mode). Fixed
/// capacity, drops the oldest frames on overflow and zero-fills on underrun
/// rather than blocking either side -- correct for a live mixer, not a
/// lossless pipe.
final class AudioRingBuffer {
    private let channelCount: Int
    private let capacity: Int
    private var channels: [[Float]]
    private var writeIndex = 0
    private var readIndex = 0
    private var filled = 0
    private let lock = NSLock()

    init(channelCount: Int, capacityFrames: Int) {
        self.channelCount = channelCount
        self.capacity = max(capacityFrames, 1)
        self.channels = Array(repeating: [Float](repeating: 0, count: self.capacity), count: channelCount)
    }

    /// Called from the CoreAudio IOProc thread.
    func write(from buffer: AVAudioPCMBuffer) {
        guard let source = buffer.floatChannelData else { return }
        let frames = Int(buffer.frameLength)
        let sourceChannels = Int(buffer.format.channelCount)

        lock.lock()
        defer { lock.unlock() }
        for f in 0..<frames {
            for ch in 0..<channelCount {
                channels[ch][writeIndex] = ch < sourceChannels ? source[ch][f] : 0
            }
            writeIndex = (writeIndex + 1) % capacity
            if filled < capacity {
                filled += 1
            } else {
                readIndex = (readIndex + 1) % capacity
            }
        }
    }

    /// Called from the mixer's timer thread. Fills exactly `frameCount`
    /// frames per channel, zero-filling on underrun.
    func read(into destinations: [UnsafeMutablePointer<Float>], frameCount: Int) {
        lock.lock()
        defer { lock.unlock() }
        let framesToRead = min(frameCount, filled)
        for f in 0..<framesToRead {
            for ch in 0..<channelCount {
                destinations[ch][f] = channels[ch][readIndex]
            }
            readIndex = (readIndex + 1) % capacity
        }
        filled -= framesToRead
        guard framesToRead < frameCount else { return }
        for f in framesToRead..<frameCount {
            for ch in 0..<channelCount {
                destinations[ch][f] = 0
            }
        }
    }
}

// MARK: - Sample mixer

/// Combines the system-audio and microphone ring buffers into one stream on a
/// fixed-interval timer, rather than driving off either capture callback
/// directly -- the two sources run on independent hardware clocks, so a
/// shared tick is simpler than trying to synchronize them. Mono mic audio is
/// broadcast across every output channel (and extra mic channels beyond the
/// output's are just dropped); good enough for the common "1-channel mic +
/// N-channel system" case this targets.
final class SampleMixer {
    private let systemBuffer: AudioRingBuffer
    private let micBuffer: AudioRingBuffer
    private let micChannelCount: Int
    private let outputFormat: AVAudioFormat
    private let framesPerTick: Int
    private let timer: DispatchSourceTimer
    private let onMixedBuffer: (AVAudioPCMBuffer) -> Void
    private let micScratch: [UnsafeMutablePointer<Float>]

    init(systemBuffer: AudioRingBuffer,
         micBuffer: AudioRingBuffer,
         micChannelCount: Int,
         outputFormat: AVAudioFormat,
         tickMilliseconds: Int = 20,
         onMixedBuffer: @escaping (AVAudioPCMBuffer) -> Void) {
        let resolvedMicChannelCount = max(micChannelCount, 1)
        let resolvedFramesPerTick = max(Int(outputFormat.sampleRate * Double(tickMilliseconds) / 1000), 1)

        self.systemBuffer = systemBuffer
        self.micBuffer = micBuffer
        self.micChannelCount = resolvedMicChannelCount
        self.outputFormat = outputFormat
        self.onMixedBuffer = onMixedBuffer
        self.framesPerTick = resolvedFramesPerTick
        self.micScratch = (0..<resolvedMicChannelCount).map { _ in
            UnsafeMutablePointer<Float>.allocate(capacity: resolvedFramesPerTick)
        }
        self.timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "nastro-tap.mixer"))
        timer.schedule(deadline: .now() + .milliseconds(tickMilliseconds), repeating: .milliseconds(tickMilliseconds))
        timer.setEventHandler { [weak self] in self?.tick() }
    }

    deinit {
        for pointer in micScratch { pointer.deallocate() }
    }

    func start() { timer.resume() }
    func stop() { timer.cancel() }

    private func tick() {
        let channelCount = Int(outputFormat.channelCount)
        guard let buffer = AVAudioPCMBuffer(pcmFormat: outputFormat, frameCapacity: AVAudioFrameCount(framesPerTick)),
              let dest = buffer.floatChannelData else { return }
        buffer.frameLength = AVAudioFrameCount(framesPerTick)

        let destPointers = (0..<channelCount).map { dest[$0] }
        systemBuffer.read(into: destPointers, frameCount: framesPerTick)
        micBuffer.read(into: micScratch, frameCount: framesPerTick)

        for ch in 0..<channelCount {
            let micChannel = micScratch[ch % micChannelCount]
            let out = dest[ch]
            for f in 0..<framesPerTick {
                out[f] = max(-1, min(1, out[f] + micChannel[f]))
            }
        }

        onMixedBuffer(buffer)
    }
}

// MARK: - Recorder

final class Recorder {
    enum Mode {
        case mixed, micOnly, systemOnly
    }

    private let outputURL: URL
    private let mode: Mode
    private let startDate = Date()

    private var audioFile: AVAudioFile?
    private let fileLock = NSLock()

    private var engine: AVAudioEngine?
    private var mixer: SampleMixer?

    private var processTapID = AudioObjectID(kAudioObjectUnknown)
    private var aggregateDeviceID = AudioObjectID(kAudioObjectUnknown)
    private var deviceProcID: AudioDeviceIOProcID?
    private var tapStreamDescription: AudioStreamBasicDescription?

    var elapsedSeconds: Double { Date().timeIntervalSince(startDate) }

    init(outputURL: URL, mode: Mode) {
        self.outputURL = outputURL
        self.mode = mode
    }

    func start() throws {
        switch mode {
        case .micOnly: try startMicOnly()
        case .systemOnly: try startSystemOnly()
        case .mixed: try startMixed()
        }
    }

    func stop() {
        mixer?.stop()
        mixer = nil

        engine?.inputNode.removeTap(onBus: 0)
        engine?.stop()
        engine = nil

        if aggregateDeviceID != AudioObjectID(kAudioObjectUnknown) {
            var status = AudioDeviceStop(aggregateDeviceID, deviceProcID)
            if status != noErr {
                logWarning("Failed to stop system audio device: OSStatus \(status)")
            }
            if let deviceProcID {
                status = AudioDeviceDestroyIOProcID(aggregateDeviceID, deviceProcID)
                if status != noErr {
                    logWarning("Failed to destroy system audio I/O proc: OSStatus \(status)")
                }
                self.deviceProcID = nil
            }
            status = AudioHardwareDestroyAggregateDevice(aggregateDeviceID)
            if status != noErr {
                logWarning("Failed to destroy aggregate device: OSStatus \(status)")
            }
            aggregateDeviceID = AudioObjectID(kAudioObjectUnknown)
        }

        if processTapID != AudioObjectID(kAudioObjectUnknown) {
            let status = AudioHardwareDestroyProcessTap(processTapID)
            if status != noErr {
                logWarning("Failed to destroy process tap: OSStatus \(status)")
            }
            processTapID = AudioObjectID(kAudioObjectUnknown)
        }

        // Releasing the last reference to AVAudioFile finalizes/closes it,
        // making the .m4a container well-formed and playable.
        fileLock.lock()
        audioFile = nil
        fileLock.unlock()
    }

    // MARK: Mic-only

    private func startMicOnly() throws {
        try ensureMicrophonePermission()

        let engine = AVAudioEngine()
        self.engine = engine

        let input = engine.inputNode
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else {
            throw micPermissionError
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: format.sampleRate, channelCount: format.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: format.isInterleaved)
        setAudioFile(file)

        input.installTap(onBus: 0, bufferSize: 4096, format: format) { [weak self] buffer, _ in
            self?.write(buffer)
        }

        engine.prepare()
        do {
            try engine.start()
        } catch {
            throw classifyMicError(error)
        }
    }

    // MARK: System-only

    private func startSystemOnly() throws {
        try setupSystemTap()

        guard var description = tapStreamDescription, let format = AVAudioFormat(streamDescription: &description) else {
            throw CaptureError.general("Failed to derive an audio format from the system audio tap.")
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: format.sampleRate, channelCount: format.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: format.isInterleaved)
        setAudioFile(file)

        try startSystemIOProc { [weak self] buffer in
            self?.write(buffer)
        }
    }

    // MARK: Mixed

    private func startMixed() throws {
        try ensureMicrophonePermission()
        try setupSystemTap()

        guard var description = tapStreamDescription, let systemFormat = AVAudioFormat(streamDescription: &description) else {
            throw CaptureError.general("Failed to derive an audio format from the system audio tap.")
        }

        let systemBuffer = AudioRingBuffer(
            channelCount: Int(systemFormat.channelCount),
            capacityFrames: Int(systemFormat.sampleRate * 2))
        try startSystemIOProc { buffer in
            systemBuffer.write(from: buffer)
        }

        // Deliberately does NOT use AVAudioEngine.connect() for the input
        // node: that call is not a throwing Swift API, and when the mic
        // isn't actually usable it raises an uncaught NSException deep in
        // CoreAudio's AudioUnit format negotiation instead of failing
        // cleanly (reproduced on this machine even after checking
        // AVAudioApplication.recordPermission first -- that status isn't a
        // reliable signal for an unbundled CLI binary). Tapping the raw
        // input node, as in mic-only mode, avoids that codepath entirely.
        let engine = AVAudioEngine()
        self.engine = engine

        let input = engine.inputNode
        let micFormat = input.outputFormat(forBus: 0)
        guard micFormat.sampleRate > 0, micFormat.channelCount > 0 else {
            throw micPermissionError
        }

        let micBuffer = AudioRingBuffer(
            channelCount: Int(micFormat.channelCount),
            capacityFrames: Int(micFormat.sampleRate * 2))
        input.installTap(onBus: 0, bufferSize: 4096, format: micFormat) { buffer, _ in
            micBuffer.write(from: buffer)
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: systemFormat.sampleRate, channelCount: systemFormat.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: false)
        setAudioFile(file)

        let outputFormat = AVAudioFormat(standardFormatWithSampleRate: systemFormat.sampleRate, channels: systemFormat.channelCount)!
        let mixer = SampleMixer(
            systemBuffer: systemBuffer,
            micBuffer: micBuffer,
            micChannelCount: Int(micFormat.channelCount),
            outputFormat: outputFormat
        ) { [weak self] buffer in
            self?.write(buffer)
        }
        self.mixer = mixer
        mixer.start()

        engine.prepare()
        do {
            try engine.start()
        } catch {
            throw classifyMicError(error)
        }
    }

    // MARK: System tap setup shared by system-only and mixed

    private func setupSystemTap() throws {
        // Empty exclude list = tap every process's output, i.e. true
        // system-wide capture rather than a specific set of processes.
        let tapDescription = CATapDescription(stereoGlobalTapButExcludeProcesses: [])
        tapDescription.uuid = UUID()
        tapDescription.muteBehavior = .unmuted

        var tapID = AudioObjectID(kAudioObjectUnknown)
        var status = AudioHardwareCreateProcessTap(tapDescription, &tapID)
        guard status == noErr else {
            throw classifyCoreAudioError(status, context: "Failed to create system audio tap")
        }
        processTapID = tapID
        tapStreamDescription = try readTapFormat(tapID)

        let systemOutputID = try readDefaultSystemOutputDevice()
        let outputUID = try readDeviceUID(systemOutputID)
        let aggregateUID = UUID().uuidString

        let description: [String: Any] = [
            kAudioAggregateDeviceNameKey: "nastro-tap-\(aggregateUID)",
            kAudioAggregateDeviceUIDKey: aggregateUID,
            kAudioAggregateDeviceMainSubDeviceKey: outputUID,
            kAudioAggregateDeviceIsPrivateKey: true,
            kAudioAggregateDeviceIsStackedKey: false,
            kAudioAggregateDeviceTapAutoStartKey: true,
            kAudioAggregateDeviceSubDeviceListKey: [
                [kAudioSubDeviceUIDKey: outputUID]
            ],
            kAudioAggregateDeviceTapListKey: [
                [
                    kAudioSubTapDriftCompensationKey: true,
                    kAudioSubTapUIDKey: tapDescription.uuid.uuidString,
                ]
            ],
        ]

        var aggregateID = AudioObjectID(kAudioObjectUnknown)
        status = AudioHardwareCreateAggregateDevice(description as CFDictionary, &aggregateID)
        guard status == noErr else {
            throw classifyCoreAudioError(status, context: "Failed to create aggregate device for system audio capture")
        }
        aggregateDeviceID = aggregateID
    }

    private func startSystemIOProc(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        guard var description = tapStreamDescription, let format = AVAudioFormat(streamDescription: &description) else {
            throw CaptureError.general("Failed to derive an audio format from the system audio tap.")
        }

        let queue = DispatchQueue(label: "nastro-tap.system-audio")
        var procID: AudioDeviceIOProcID?
        let createStatus = AudioDeviceCreateIOProcIDWithBlock(&procID, aggregateDeviceID, queue) { _, inInputData, _, _, _ in
            guard let buffer = AVAudioPCMBuffer(pcmFormat: format, bufferListNoCopy: inInputData, deallocator: nil) else { return }
            onBuffer(buffer)
        }
        guard createStatus == noErr, let procID else {
            throw classifyCoreAudioError(createStatus, context: "Failed to create system audio I/O proc")
        }
        deviceProcID = procID

        let startStatus = AudioDeviceStart(aggregateDeviceID, procID)
        guard startStatus == noErr else {
            throw classifyCoreAudioError(startStatus, context: "Failed to start system audio capture device")
        }
    }

    // MARK: File writing

    private func setAudioFile(_ file: AVAudioFile) {
        fileLock.lock()
        audioFile = file
        fileLock.unlock()
    }

    private func write(_ buffer: AVAudioPCMBuffer) {
        fileLock.lock()
        defer { fileLock.unlock() }
        guard let audioFile else { return }
        do {
            try audioFile.write(from: buffer)
        } catch {
            logWarning("Failed to write audio buffer: \(error)")
        }
    }
}

private func logWarning(_ message: String) {
    FileHandle.standardError.write("Warning: \(message)\n".data(using: .utf8)!)
}

// MARK: - Entry point

private func fail(_ message: String, code: Int32 = 1) -> Never {
    FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
    exit(code)
}

let rawArguments = Array(CommandLine.arguments.dropFirst())
let flags = Set(rawArguments.filter { $0.hasPrefix("--") })
let positional = rawArguments.filter { !$0.hasPrefix("--") }

guard let outputPath = positional.first else {
    fail("Usage: nastro-tap <audio-output-path> [--mic-only|--system-only]")
}

let micOnly = flags.contains("--mic-only")
let systemOnly = flags.contains("--system-only")
if micOnly && systemOnly {
    fail("Error: --mic-only and --system-only are mutually exclusive.")
}

let mode: Recorder.Mode = micOnly ? .micOnly : (systemOnly ? .systemOnly : .mixed)
let outputURL = URL(fileURLWithPath: outputPath)
let recorder = Recorder(outputURL: outputURL, mode: mode)

do {
    try recorder.start()
} catch let error as CaptureError {
    switch error {
    case .permissionDenied(let message):
        fail(message, code: 2)
    case .general(let message):
        fail(message, code: 1)
    }
} catch {
    fail("Failed to start audio capture: \(error.localizedDescription)")
}

func writeMetadata() {
    let duration = recorder.elapsedSeconds
    let metadataURL = outputURL.deletingLastPathComponent().appendingPathComponent("metadata.json")
    let json = String(format: "{\"duration_seconds\": %.3f}\n", duration)
    do {
        try json.write(to: metadataURL, atomically: true, encoding: .utf8)
    } catch {
        logWarning("Failed to write metadata.json: \(error)")
    }
}

func shutdown() -> Never {
    recorder.stop()
    writeMetadata()
    exit(0)
}

// Suppress the default terminating action first, then handle the signal via
// DispatchSource on the main queue: a raw C signal handler runs in an
// async-signal-unsafe context (can't safely touch Swift runtime/ARC state),
// so this hands the actual cleanup off to the main run loop instead.
signal(SIGINT, SIG_IGN)
signal(SIGTERM, SIG_IGN)

let sigintSource = DispatchSource.makeSignalSource(signal: SIGINT, queue: .main)
sigintSource.setEventHandler { shutdown() }
sigintSource.resume()

let sigtermSource = DispatchSource.makeSignalSource(signal: SIGTERM, queue: .main)
sigtermSource.setEventHandler { shutdown() }
sigtermSource.resume()

RunLoop.main.run()
