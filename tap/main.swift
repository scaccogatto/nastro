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
import AppKit
import CoreGraphics

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

/// `kAudioTapPropertyFormat`'s sample rate is unreliable (see the long
/// comment in `setupSystemTap`); `kAudioDevicePropertyNominalSampleRate` on
/// the aggregate device is what CoreAudio actually delivers frames at.
private func readNominalSampleRate(_ deviceID: AudioObjectID) throws -> Double {
    var address = AudioObjectPropertyAddress(
        mSelector: kAudioDevicePropertyNominalSampleRate,
        mScope: kAudioObjectPropertyScopeGlobal,
        mElement: kAudioObjectPropertyElementMain)
    var rate: Double = 0
    var size = UInt32(MemoryLayout<Double>.size)
    let status = AudioObjectGetPropertyData(deviceID, &address, 0, nil, &size, &rate)
    guard status == noErr else {
        throw CaptureError.general("Failed to read nominal sample rate: OSStatus \(status)")
    }
    return rate
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

// MARK: - Rate correction

private let standardSampleRates: [Double] = [8000, 11025, 16000, 22050, 24000, 32000, 44100, 48000, 88200, 96000, 176400, 192000]

/// Snaps a measured rate to the nearest standard audio rate when it is within
/// 1%, so ordinary jitter does not leave the file at 43987 Hz.
func snapToStandardRate(_ rate: Double) -> Double {
    guard rate > 0 else { return rate }
    for standard in standardSampleRates where abs(rate - standard) / standard <= 0.01 {
        return standard
    }
    return rate
}

/// Frames the mixer owes the output file at `elapsed` seconds, given what it
/// has already written. Clamped so a stalled timer catches up gradually
/// instead of emitting one enormous burst.
func framesOwed(elapsed: Double, sampleRate: Double, alreadyEmitted: Int, maxBurst: Int) -> Int {
    guard elapsed > 0 else { return 0 }
    let owed = Int(elapsed * sampleRate) - alreadyEmitted
    return max(0, min(owed, maxBurst))
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

// MARK: - Format conversion

/// Converts capture buffers into a fixed target format (standard, i.e.
/// float32 non-interleaved, sample rate fixed, channel count preserved from
/// the input) so every ingestion point can assume a stable rate regardless
/// of what the source device actually delivers -- see the tap-format-lies
/// comment in `setupSystemTap` for why this exists. Always converting to a
/// standard non-interleaved format also fixes a latent bug: `AudioRingBuffer
/// .write` reads `floatChannelData`, which is nil for interleaved buffers.
///
/// Used from exactly one capture thread per instance (the IOProc queue or
/// the AVAudioEngine tap queue that owns it) -- no locking inside.
final class FormatConverter {
    private let targetSampleRate: Double
    private var converter: AVAudioConverter?
    private var sourceFormat: AVAudioFormat?
    private var cachedTargetFormat: AVAudioFormat?
    private var cachedTargetChannels: AVAudioChannelCount?
    private var lastConversionFailed = false

    init(targetSampleRate: Double) {
        self.targetSampleRate = targetSampleRate
    }

    /// The format callers should build their AVAudioFile from -- exactly
    /// what `convert` emits for the given channel count. nil only if
    /// AVFoundation rejects the channel count (never happens for channel
    /// counts coming from an already-validated tap/mic format, but callers
    /// still handle it rather than force-unwrapping).
    func targetFormat(channels: AVAudioChannelCount) -> AVAudioFormat? {
        if let cached = cachedTargetFormat, cachedTargetChannels == channels {
            return cached
        }
        guard let format = AVAudioFormat(standardFormatWithSampleRate: targetSampleRate, channels: channels) else {
            return nil
        }
        cachedTargetFormat = format
        cachedTargetChannels = channels
        return format
    }

    /// Returns nil (never crashes) when the target format can't be built or
    /// conversion fails; callers skip a nil buffer.
    func convert(_ buffer: AVAudioPCMBuffer) -> AVAudioPCMBuffer? {
        guard let target = targetFormat(channels: buffer.format.channelCount) else {
            if !lastConversionFailed {
                logWarning("Could not build target format for \(buffer.format.channelCount)ch audio.")
                lastConversionFailed = true
            }
            return nil
        }

        // Fast path: already exactly the target format, no copy needed.
        if buffer.format.sampleRate == target.sampleRate,
           buffer.format.channelCount == target.channelCount,
           !buffer.format.isInterleaved,
           buffer.format.commonFormat == .pcmFormatFloat32 {
            return buffer
        }

        if converter == nil || sourceFormat != buffer.format {
            converter = AVAudioConverter(from: buffer.format, to: target)
            sourceFormat = buffer.format
            logInfo("Rebuilt format converter: source now \(buffer.format.sampleRate)Hz/\(buffer.format.channelCount)ch -> target \(target.sampleRate)Hz/\(target.channelCount)ch")
        }
        guard let converter else { return nil }

        let outputCapacity = AVAudioFrameCount(
            (Double(buffer.frameLength) * target.sampleRate / buffer.format.sampleRate).rounded(.up)) + 64
        guard let output = AVAudioPCMBuffer(pcmFormat: target, frameCapacity: outputCapacity) else { return nil }

        var consumed = false
        var conversionError: NSError?
        let status = converter.convert(to: output, error: &conversionError) { _, outStatus in
            if consumed {
                outStatus.pointee = .noDataNow
                return nil
            }
            consumed = true
            outStatus.pointee = .haveData
            return buffer
        }

        switch status {
        case .haveData, .inputRanDry:
            lastConversionFailed = false
            return output
        default:
            if !lastConversionFailed {
                logWarning("Format conversion failed: \(conversionError?.localizedDescription ?? "unknown error")")
                lastConversionFailed = true
            }
            return nil
        }
    }
}

// MARK: - Rate watcher

/// Measures how many frames a source actually delivers per second of wall
/// clock and reports a corrected rate when the assumed one is grossly wrong.
/// The tap format already lied once (see `setupSystemTap`), so this is the
/// safety net under the aggregate's nominal-rate reading, not a replacement
/// for it.
final class RateWatcher {
    private var assumedRate: Double
    private var lastDeviatingMeasurement: Double?

    init(assumedRate: Double) {
        self.assumedRate = assumedRate
    }

    /// Pure decision step: feeds one window's worth of counted frames and the
    /// wall-clock seconds they took. Returns a new assumed rate when the
    /// measurement disagrees with the current assumption by more than 2% for
    /// two consecutive windows, nil otherwise.
    func update(frames: Int, elapsed: Double) -> Double? {
        guard elapsed > 0, frames > 0 else {
            lastDeviatingMeasurement = nil
            return nil
        }
        let measured = Double(frames) / elapsed

        guard abs((measured - assumedRate) / assumedRate) > 0.02 else {
            lastDeviatingMeasurement = nil
            return nil
        }

        // Two windows deviating in the same direction is not enough: a device
        // transition delivers a disturbed window that can read as any rate.
        // The two measurements must also agree with each other, which only a
        // genuinely wrong assumed rate produces repeatably.
        guard let previous = lastDeviatingMeasurement,
              abs(measured - previous) / previous <= 0.01 else {
            lastDeviatingMeasurement = measured
            return nil
        }

        lastDeviatingMeasurement = nil
        let corrected = snapToStandardRate(measured)
        assumedRate = corrected
        return corrected
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
    private let maxBurstFrames: Int
    private let timer: DispatchSourceTimer
    private let onMixedBuffer: (AVAudioPCMBuffer) -> Void
    private let micScratch: [UnsafeMutablePointer<Float>]
    private var startTime: Date?
    private var framesEmitted = 0

    init(systemBuffer: AudioRingBuffer,
         micBuffer: AudioRingBuffer,
         micChannelCount: Int,
         outputFormat: AVAudioFormat,
         tickMilliseconds: Int = 20,
         onMixedBuffer: @escaping (AVAudioPCMBuffer) -> Void) {
        let resolvedMicChannelCount = max(micChannelCount, 1)
        let resolvedFramesPerTick = max(Int(outputFormat.sampleRate * Double(tickMilliseconds) / 1000), 1)
        let resolvedMaxBurst = resolvedFramesPerTick * 4

        self.systemBuffer = systemBuffer
        self.micBuffer = micBuffer
        self.micChannelCount = resolvedMicChannelCount
        self.outputFormat = outputFormat
        self.onMixedBuffer = onMixedBuffer
        self.framesPerTick = resolvedFramesPerTick
        self.maxBurstFrames = resolvedMaxBurst
        self.micScratch = (0..<resolvedMicChannelCount).map { _ in
            UnsafeMutablePointer<Float>.allocate(capacity: resolvedMaxBurst)
        }
        self.timer = DispatchSource.makeTimerSource(queue: DispatchQueue(label: "nastro-tap.mixer"))
        timer.schedule(deadline: .now() + .milliseconds(tickMilliseconds), repeating: .milliseconds(tickMilliseconds))
        timer.setEventHandler { [weak self] in self?.tick() }
    }

    deinit {
        for pointer in micScratch { pointer.deallocate() }
    }

    func start() {
        startTime = Date()
        framesEmitted = 0
        timer.resume()
    }
    func stop() { timer.cancel() }

    /// Emits however many frames the wall clock says are owed since start(),
    /// rather than a fixed count per tick -- the 20ms DispatchSourceTimer
    /// isn't precise enough on its own, and a fixed count would drift
    /// against real time over a long recording.
    private func tick() {
        guard let startTime else { return }
        let elapsed = Date().timeIntervalSince(startTime)
        let owed = framesOwed(elapsed: elapsed, sampleRate: outputFormat.sampleRate, alreadyEmitted: framesEmitted, maxBurst: maxBurstFrames)
        guard owed > 0 else { return }

        let channelCount = Int(outputFormat.channelCount)
        guard let buffer = AVAudioPCMBuffer(pcmFormat: outputFormat, frameCapacity: AVAudioFrameCount(owed)),
              let dest = buffer.floatChannelData else { return }
        buffer.frameLength = AVAudioFrameCount(owed)

        let destPointers = (0..<channelCount).map { dest[$0] }
        systemBuffer.read(into: destPointers, frameCount: owed)
        micBuffer.read(into: micScratch, frameCount: owed)

        for ch in 0..<channelCount {
            let micChannel = micScratch[ch % micChannelCount]
            let out = dest[ch]
            for f in 0..<owed {
                out[f] = max(-1, min(1, out[f] + micChannel[f]))
            }
        }

        framesEmitted += owed
        onMixedBuffer(buffer)
    }
}

// MARK: - Level meter

/// Accumulates RMS audio level over an interval (fed from a capture callback
/// thread), read and reset once a second from the main-queue level timer.
final class LevelMeter {
    private let lock = NSLock()
    private var sumSquares: Double = 0
    private var sampleCount: Int = 0

    func accumulate(_ buffer: AVAudioPCMBuffer) {
        guard let channels = buffer.floatChannelData else { return }
        let frames = Int(buffer.frameLength)
        let channelCount = Int(buffer.format.channelCount)
        var sum: Double = 0
        for ch in 0..<channelCount {
            let data = channels[ch]
            for f in 0..<frames {
                let s = Double(data[f])
                sum += s * s
            }
        }

        lock.lock()
        sumSquares += sum
        sampleCount += frames * channelCount
        lock.unlock()
    }

    /// Returns the normalized (0...1) RMS level for the accumulated interval
    /// and resets the accumulator for the next one.
    func consume() -> Double {
        lock.lock()
        defer {
            sumSquares = 0
            sampleCount = 0
            lock.unlock()
        }
        guard sampleCount > 0 else { return 0 }
        return min(1, (sumSquares / Double(sampleCount)).squareRoot())
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

    private let systemLevel = LevelMeter()
    private let micLevel = LevelMeter()

    private var processTapID = AudioObjectID(kAudioObjectUnknown)
    private var aggregateDeviceID = AudioObjectID(kAudioObjectUnknown)
    private var deviceProcID: AudioDeviceIOProcID?
    private var tapStreamDescription: AudioStreamBasicDescription?

    // The tap ASBD's sample rate is unreliable (see setupSystemTap); this is
    // the current best estimate of what the aggregate device actually runs
    // at, read/written from both the main-queue timer and the IOProc queue.
    private var systemSourceRate: Double = 48000
    private let systemSourceRateLock = NSLock()
    private var cachedSourceFormat: AVAudioFormat?
    private var cachedSourceFormatRate: Double = 0
    private var rateWatcher: RateWatcher?
    private var rateWindowFrames = 0
    private var rateWindowSeconds = 0
    private var systemAccumulatedFrames = 0
    private var rateWindowResetRequested = false
    private let systemAccumulatedFramesLock = NSLock()

    private var systemFrameConverter: FormatConverter?
    private var micFrameConverter: FormatConverter?
    // The mode-specific "what to do with a converted buffer" closures,
    // stashed so device-change rebuilds can reinstall the exact same
    // behavior instead of duplicating it.
    private var currentSystemOnBuffer: ((AVAudioPCMBuffer) -> Void)?
    private var currentMicOnBuffer: ((AVAudioPCMBuffer) -> Void)?

    private var defaultDeviceListenerBlock: AudioObjectPropertyListenerBlock?
    private var nominalRateListenerBlock: AudioObjectPropertyListenerBlock?
    private var engineConfigObserver: NSObjectProtocol?
    private var isRebuildingSystemCapture = false
    // Set inside systemCaptureQueue during stop(); rebuild and mic-change
    // handlers check it so nothing resurrects capture after shutdown.
    private var isStopped = false
    private let systemCaptureQueue = DispatchQueue(label: "nastro-tap.system-capture-events")

    var elapsedSeconds: Double { Date().timeIntervalSince(startDate) }

    /// Consumes (and resets) the last second of levels for whichever
    /// source(s) this recorder's mode actually captures -- nil for the
    /// inactive source in mic-only/system-only mode.
    func consumeLevels() -> (sys: Double?, mic: Double?) {
        switch mode {
        case .micOnly: return (nil, micLevel.consume())
        case .systemOnly: return (systemLevel.consume(), nil)
        case .mixed: return (systemLevel.consume(), micLevel.consume())
        }
    }

    private func currentSystemSourceRate() -> Double {
        systemSourceRateLock.lock(); defer { systemSourceRateLock.unlock() }
        return systemSourceRate
    }

    private func setSystemSourceRate(_ rate: Double) {
        systemSourceRateLock.lock(); systemSourceRate = rate; systemSourceRateLock.unlock()
    }

    /// Throws away the measurement window in flight. Called from whichever
    /// queue notices that capture changed underneath it, hence the lock.
    private func requestRateWindowReset() {
        systemAccumulatedFramesLock.lock()
        systemAccumulatedFrames = 0
        rateWindowResetRequested = true
        systemAccumulatedFramesLock.unlock()
    }

    /// Called once a second by the top-level level timer. Every second
    /// counts as a half-window; two consecutive calls form the 2s window
    /// RateWatcher evaluates. Only wired for system-tap capture (mixed /
    /// system-only) -- the mic's AVAudioEngine format is trusted as-is, so
    /// relabelling its already-correct buffers would only introduce error.
    /// Runs only on the main queue (the level timer), so rateWindow* need no
    /// lock of their own.
    func tickRateWatcher() {
        guard mode == .systemOnly || mode == .mixed else { return }
        systemAccumulatedFramesLock.lock()
        let frames = systemAccumulatedFrames
        let resetRequested = rateWindowResetRequested
        systemAccumulatedFrames = 0
        rateWindowResetRequested = false
        systemAccumulatedFramesLock.unlock()

        // A window that straddles a device rebuild or a rate change measures
        // the transition, not the device; drop it rather than correct on it.
        if resetRequested {
            rateWindowFrames = 0
            rateWindowSeconds = 0
            return
        }

        rateWindowFrames += frames
        rateWindowSeconds += 1
        guard rateWindowSeconds >= 2 else { return }
        defer { rateWindowFrames = 0; rateWindowSeconds = 0 }

        guard let corrected = rateWatcher?.update(frames: rateWindowFrames, elapsed: Double(rateWindowSeconds)) else { return }
        setSystemSourceRate(corrected)
        logInfo("System source rate corrected to \(Int(corrected)) Hz by runtime measurement.")
    }

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

        removeMicConfigChangeObserver()
        removeSystemCaptureChangeListeners()

        // Serialized onto the queue that rebuilds run on: a device change
        // firing while we shut down would otherwise rebuild the very tap and
        // engine this is destroying, on top of a half-torn-down recorder.
        systemCaptureQueue.sync {
            isStopped = true
            engine?.inputNode.removeTap(onBus: 0)
            engine?.stop()
            engine = nil
            teardownSystemCapture()
        }

        // Releasing the last reference to AVAudioFile finalizes/closes it,
        // making the .m4a container well-formed and playable.
        fileLock.lock()
        audioFile = nil
        fileLock.unlock()
    }

    /// Tears down the IOProc, aggregate device, and process tap -- shared by
    /// stop() and by the default-output-device-changed rebuild path.
    private func teardownSystemCapture() {
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
            removeNominalRateListener()
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

        // The output file's rate is fixed at the mic's *initial* rate for
        // the life of the recording; a FormatConverter absorbs any later mic
        // format change (route switch, Bluetooth profile change) instead of
        // every subsequent write failing against a stale AVAudioFile format.
        let converter = FormatConverter(targetSampleRate: format.sampleRate)
        micFrameConverter = converter
        guard let targetFormat = converter.targetFormat(channels: format.channelCount) else {
            throw CaptureError.general("Failed to build an output audio format for \(format.channelCount)ch mic audio.")
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: targetFormat.sampleRate, channelCount: targetFormat.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: false)
        setAudioFile(file)
        logDiagnostics(out: targetFormat, tap: nil, deviceRate: nil, mic: format)

        let onBuffer: (AVAudioPCMBuffer) -> Void = { [weak self] buffer in
            self?.micLevel.accumulate(buffer)
            self?.write(buffer)
        }
        currentMicOnBuffer = onBuffer
        input.installTap(onBus: 0, bufferSize: 4096, format: format) { [weak self] buffer, _ in
            guard let converted = self?.micFrameConverter?.convert(buffer) else { return }
            self?.currentMicOnBuffer?(converted)
        }

        engine.prepare()
        do {
            try engine.start()
        } catch {
            throw classifyMicError(error)
        }

        addMicConfigChangeObserver()
    }

    // MARK: System-only

    private func startSystemOnly() throws {
        try setupSystemTap()

        let outputRate = currentSystemSourceRate()
        let channels = AVAudioChannelCount(tapStreamDescription?.mChannelsPerFrame ?? 2)
        let converter = FormatConverter(targetSampleRate: outputRate)
        systemFrameConverter = converter
        guard let targetFormat = converter.targetFormat(channels: channels) else {
            throw CaptureError.general("Failed to build an output audio format for \(channels)ch system audio.")
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: targetFormat.sampleRate, channelCount: targetFormat.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: false)
        setAudioFile(file)
        logDiagnostics(out: targetFormat, tap: tapStreamDescription, deviceRate: outputRate, mic: nil)

        let onBuffer: (AVAudioPCMBuffer) -> Void = { [weak self] buffer in
            self?.systemLevel.accumulate(buffer)
            self?.write(buffer)
        }
        currentSystemOnBuffer = onBuffer
        try startSystemIOProc { [weak self] buffer in
            guard let converted = self?.systemFrameConverter?.convert(buffer) else { return }
            self?.currentSystemOnBuffer?(converted)
        }

        addSystemCaptureChangeListeners()
    }

    // MARK: Mixed

    private func startMixed() throws {
        try ensureMicrophonePermission()
        try setupSystemTap()

        let outputRate = currentSystemSourceRate()
        let systemChannels = AVAudioChannelCount(tapStreamDescription?.mChannelsPerFrame ?? 2)
        let systemConverter = FormatConverter(targetSampleRate: outputRate)
        systemFrameConverter = systemConverter
        guard let outputFormat = systemConverter.targetFormat(channels: systemChannels) else {
            throw CaptureError.general("Failed to build an output audio format for \(systemChannels)ch system audio.")
        }

        let systemBuffer = AudioRingBuffer(
            channelCount: Int(systemChannels),
            capacityFrames: Int(outputRate * 2))
        let systemOnBuffer: (AVAudioPCMBuffer) -> Void = { [weak self] buffer in
            self?.systemLevel.accumulate(buffer)
            systemBuffer.write(from: buffer)
        }
        currentSystemOnBuffer = systemOnBuffer
        try startSystemIOProc { [weak self] buffer in
            guard let converted = self?.systemFrameConverter?.convert(buffer) else { return }
            self?.currentSystemOnBuffer?(converted)
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

        let micConverter = FormatConverter(targetSampleRate: outputRate)
        micFrameConverter = micConverter
        let micBuffer = AudioRingBuffer(
            channelCount: Int(micFormat.channelCount),
            capacityFrames: Int(outputRate * 2))
        let micOnBuffer: (AVAudioPCMBuffer) -> Void = { [weak self] buffer in
            self?.micLevel.accumulate(buffer)
            micBuffer.write(from: buffer)
        }
        currentMicOnBuffer = micOnBuffer
        input.installTap(onBus: 0, bufferSize: 4096, format: micFormat) { [weak self] buffer, _ in
            guard let converted = self?.micFrameConverter?.convert(buffer) else { return }
            self?.currentMicOnBuffer?(converted)
        }

        let file = try AVAudioFile(
            forWriting: outputURL,
            settings: aacSettings(sampleRate: outputFormat.sampleRate, channelCount: outputFormat.channelCount),
            commonFormat: .pcmFormatFloat32,
            interleaved: false)
        setAudioFile(file)
        logDiagnostics(out: outputFormat, tap: tapStreamDescription, deviceRate: outputRate, mic: micFormat)

        // Ring buffer capacities above and the mixer's output format are both
        // pinned to outputRate, the corrected system source rate -- not the
        // (unreliable) raw tap rate. SampleMixer's mono-broadcast logic for
        // the mic channel is unaffected: conversion never changes channel
        // counts, only sample rate.
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

        addSystemCaptureChangeListeners()
        addMicConfigChangeObserver()
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

        // kAudioTapPropertyFormat's sample rate is unreliable: measured on
        // this machine it reports 48000 Hz for a tap on a device actually
        // running at 44100 Hz, both before and after aggregate creation, with
        // frames arriving at the true ~44100/s rate the whole time. The
        // aggregate device's own nominal rate is what CoreAudio actually
        // delivers frames at, so that -- not the tap ASBD -- is the source of
        // truth; RateWatcher (wired in start*) is the runtime safety net
        // under this reading, in case the nominal rate itself drifts later.
        do {
            let nominalRate = try readNominalSampleRate(aggregateID)
            if nominalRate > 0 {
                setSystemSourceRate(nominalRate)
            } else {
                setSystemSourceRate(tapStreamDescription?.mSampleRate ?? 48000)
                logWarning("Aggregate device reported a non-positive nominal sample rate; falling back to the (unreliable) tap format rate.")
            }
        } catch {
            setSystemSourceRate(tapStreamDescription?.mSampleRate ?? 48000)
            logWarning("Failed to read aggregate device nominal sample rate (\(error)); falling back to the (unreliable) tap format rate.")
        }
        rateWatcher = RateWatcher(assumedRate: currentSystemSourceRate())
        requestRateWindowReset()
    }

    /// The format IOProc buffers should be wrapped in: the tap's ASBD shape
    /// (channel layout, sample format) with the sample rate replaced by the
    /// current best estimate of the aggregate device's real rate -- never
    /// the tap ASBD's own (unreliable) rate field.
    private func currentSystemSourceFormat() -> AVAudioFormat? {
        // Called from the IOProc on every buffer, so the format object is
        // cached and only rebuilt when the rate estimate actually moves.
        let rate = currentSystemSourceRate()
        systemSourceRateLock.lock()
        defer { systemSourceRateLock.unlock() }
        if let cached = cachedSourceFormat, cachedSourceFormatRate == rate {
            return cached
        }
        guard var description = tapStreamDescription else { return nil }
        description.mSampleRate = rate
        guard let format = AVAudioFormat(streamDescription: &description) else { return nil }
        cachedSourceFormat = format
        cachedSourceFormatRate = rate
        return format
    }

    private func startSystemIOProc(_ onBuffer: @escaping (AVAudioPCMBuffer) -> Void) throws {
        guard tapStreamDescription != nil, currentSystemSourceFormat() != nil else {
            throw CaptureError.general("Failed to derive an audio format from the system audio tap.")
        }

        let queue = DispatchQueue(label: "nastro-tap.system-audio")
        var procID: AudioDeviceIOProcID?
        let createStatus = AudioDeviceCreateIOProcIDWithBlock(&procID, aggregateDeviceID, queue) { [weak self] _, inInputData, _, _, _ in
            guard let self, let format = self.currentSystemSourceFormat() else { return }
            guard let buffer = AVAudioPCMBuffer(pcmFormat: format, bufferListNoCopy: inInputData, deallocator: nil) else { return }
            self.systemAccumulatedFramesLock.lock()
            self.systemAccumulatedFrames += Int(buffer.frameLength)
            self.systemAccumulatedFramesLock.unlock()
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

    // MARK: Device change handling

    /// Fired when the user switches the system's default output device
    /// (headphones unplugged, speakers selected, etc). Rebuilds the tap
    /// against the new device so capture keeps going instead of silently
    /// tapping a now-stale (possibly disconnected) device. Only installed in
    /// modes that use the system tap.
    private func addSystemCaptureChangeListeners() {
        var address = AudioObjectPropertyAddress(
            mSelector: kAudioHardwarePropertyDefaultSystemOutputDevice,
            mScope: kAudioObjectPropertyScopeGlobal,
            mElement: kAudioObjectPropertyElementMain)
        let block: AudioObjectPropertyListenerBlock = { [weak self] _, _ in
            self?.rebuildSystemCapture()
        }
        defaultDeviceListenerBlock = block
        let status = AudioObjectAddPropertyListenerBlock(AudioObjectID(kAudioObjectSystemObject), &address, systemCaptureQueue, block)
        if status != noErr {
            logWarning("Failed to install default-output-device listener: OSStatus \(status)")
        }
        addNominalRateListener()
    }

    private func removeSystemCaptureChangeListeners() {
        guard let block = defaultDeviceListenerBlock else { return }
        var address = AudioObjectPropertyAddress(
            mSelector: kAudioHardwarePropertyDefaultSystemOutputDevice,
            mScope: kAudioObjectPropertyScopeGlobal,
            mElement: kAudioObjectPropertyElementMain)
        AudioObjectRemovePropertyListenerBlock(AudioObjectID(kAudioObjectSystemObject), &address, systemCaptureQueue, block)
        defaultDeviceListenerBlock = nil
    }

    private func addNominalRateListener() {
        guard aggregateDeviceID != AudioObjectID(kAudioObjectUnknown) else { return }
        var address = AudioObjectPropertyAddress(
            mSelector: kAudioDevicePropertyNominalSampleRate,
            mScope: kAudioObjectPropertyScopeGlobal,
            mElement: kAudioObjectPropertyElementMain)
        let block: AudioObjectPropertyListenerBlock = { [weak self] _, _ in
            guard let self else { return }
            if let rate = try? readNominalSampleRate(self.aggregateDeviceID), rate > 0 {
                self.setSystemSourceRate(rate)
                self.rateWatcher = RateWatcher(assumedRate: rate)
                self.requestRateWindowReset()
                logInfo("Aggregate device nominal sample rate changed to \(Int(rate)) Hz.")
            }
        }
        nominalRateListenerBlock = block
        let status = AudioObjectAddPropertyListenerBlock(aggregateDeviceID, &address, systemCaptureQueue, block)
        if status != noErr {
            logWarning("Failed to install nominal-rate listener: OSStatus \(status)")
        }
    }

    private func removeNominalRateListener() {
        guard let block = nominalRateListenerBlock, aggregateDeviceID != AudioObjectID(kAudioObjectUnknown) else {
            nominalRateListenerBlock = nil
            return
        }
        var address = AudioObjectPropertyAddress(
            mSelector: kAudioDevicePropertyNominalSampleRate,
            mScope: kAudioObjectPropertyScopeGlobal,
            mElement: kAudioObjectPropertyElementMain)
        AudioObjectRemovePropertyListenerBlock(aggregateDeviceID, &address, systemCaptureQueue, block)
        nominalRateListenerBlock = nil
    }

    /// Rebuilds the system tap + aggregate + IOProc against whatever is now
    /// the default output device. A failed rebuild logs and leaves the
    /// recording running (mic-only audio keeps flowing in mixed mode,
    /// silence in system-only) rather than exiting -- this runs mid-recording,
    /// long after startup failures are allowed to be fatal.
    private func rebuildSystemCapture() {
        systemCaptureQueue.async { [weak self] in
            guard let self, !self.isStopped else { return }
            guard !self.isRebuildingSystemCapture else { return }
            self.isRebuildingSystemCapture = true
            defer { self.isRebuildingSystemCapture = false }

            self.removeNominalRateListener()
            self.teardownSystemCapture()

            guard let onBuffer = self.currentSystemOnBuffer else { return }
            do {
                try self.setupSystemTap()
                try self.startSystemIOProc { [weak self] buffer in
                    guard let converted = self?.systemFrameConverter?.convert(buffer) else { return }
                    onBuffer(converted)
                }
                self.addNominalRateListener()
                logInfo("System capture rebuilt after default output device change (device=\(Int(self.currentSystemSourceRate())) Hz).")
            } catch {
                logWarning("Failed to rebuild system capture after device change: \(error)")
            }
        }
    }

    // MARK: Mic route/format changes

    private func addMicConfigChangeObserver() {
        engineConfigObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange,
            object: engine,
            queue: nil
        ) { [weak self] _ in
            self?.systemCaptureQueue.async { self?.handleMicConfigChange() }
        }
    }

    private func removeMicConfigChangeObserver() {
        guard let engineConfigObserver else { return }
        NotificationCenter.default.removeObserver(engineConfigObserver)
        self.engineConfigObserver = nil
    }

    private func handleMicConfigChange() {
        guard !isStopped, let engine, let onBuffer = currentMicOnBuffer else { return }
        guard micTapFormatIsUsable(engine) else {
            logWarning("Mic route changed to a format with no rate/channels (device likely disconnected); leaving prior tap in place.")
            return
        }

        engine.inputNode.removeTap(onBus: 0)
        installMicTap(on: engine, onBuffer: onBuffer)
        if engine.isRunning {
            logInfo("Mic route changed; retapped at \(Int(engine.inputNode.outputFormat(forBus: 0).sampleRate))Hz.")
            return
        }

        // reset() + prepare() before start(): the configuration change tore
        // down the engine's input chain, and start() alone fails with -10868.
        engine.reset()
        engine.prepare()
        do {
            try engine.start()
            logInfo("Mic route changed; retapped at \(Int(engine.inputNode.outputFormat(forBus: 0).sampleRate))Hz.")
            return
        } catch {
            logWarning("Audio engine would not restart after the mic route change (\(error)); rebuilding it.")
        }

        // Last resort: the old engine's input chain can stay wedged once the
        // route changed underneath it, and a half-dead engine means no
        // microphone for the rest of the recording. A fresh one costs a
        // fraction of a second and does come back.
        engine.stop()
        guard !isStopped else { return }
        let replacement = AVAudioEngine()
        self.engine = replacement
        // The observer was registered against the engine we just discarded,
        // so without re-registering it a second route change would go unseen.
        removeMicConfigChangeObserver()
        addMicConfigChangeObserver()
        guard micTapFormatIsUsable(replacement) else {
            logWarning("Replacement audio engine reports an unusable input format; microphone capture is stopped.")
            return
        }
        installMicTap(on: replacement, onBuffer: onBuffer)
        replacement.prepare()
        do {
            try replacement.start()
            logInfo("Mic capture rebuilt on a fresh engine at \(Int(replacement.inputNode.outputFormat(forBus: 0).sampleRate))Hz.")
        } catch {
            logWarning("Failed to restart microphone capture after a route change: \(error)")
        }
    }

    private func micTapFormatIsUsable(_ engine: AVAudioEngine) -> Bool {
        let format = engine.inputNode.outputFormat(forBus: 0)
        return format.sampleRate > 0 && format.channelCount > 0
    }

    /// format: nil makes AVAudioEngine read the bus's format at install time.
    /// Handing it a format read a moment earlier raises an uncatchable
    /// NSException ("Failed to create tap due to format mismatch") whenever
    /// the route settles in between -- reproduced by changing the output
    /// device's sample rate mid-recording, which killed the whole capture
    /// process. The FormatConverter adapts to whatever the buffers actually
    /// arrive as, so nothing here needs to know the format up front.
    private func installMicTap(on engine: AVAudioEngine, onBuffer: @escaping (AVAudioPCMBuffer) -> Void) {
        engine.inputNode.installTap(onBus: 0, bufferSize: 4096, format: nil) { [weak self] buffer, _ in
            guard let converted = self?.micFrameConverter?.convert(buffer) else { return }
            onBuffer(converted)
        }
    }

    // MARK: Diagnostics

    /// One stderr line describing the resolved formats -- emitted at startup
    /// and after every rebuild (device switch, rate correction). Never
    /// touches stdout: the `level ...` lines are a parsed contract with the
    /// Go side.
    private func logDiagnostics(out: AVAudioFormat, tap: AudioStreamBasicDescription?, deviceRate: Double?, mic: AVAudioFormat?) {
        var parts = ["out=\(Int(out.sampleRate))/\(out.channelCount)"]
        if let tap { parts.append("tap=\(Int(tap.mSampleRate))") }
        if let deviceRate { parts.append("device=\(Int(deviceRate))") }
        if let mic { parts.append("mic=\(Int(mic.sampleRate))/\(mic.channelCount)") }
        logInfo("formats " + parts.joined(separator: " "))
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

private func logInfo(_ message: String) {
    FileHandle.standardError.write("Info: \(message)\n".data(using: .utf8)!)
}

// MARK: - Menu bar indicator

/// Cosmetic only: capture is the primary job, this is best-effort on top of
/// it. `CGSessionCopyCurrentDictionary` returns nil (no "on console" key)
/// when the process has no attached graphical session -- e.g. run over SSH
/// or from a launchd daemon context -- which is exactly when creating a
/// status item would be pointless (nothing to draw it on).
private func hasGraphicalSession() -> Bool {
    guard let info = CGSessionCopyCurrentDictionary() as? [String: Any] else { return false }
    return (info["kCGSSessionOnConsoleKey"] as? Bool) ?? false
}

private var recordingStatusItem: NSStatusItem?

private func showRecordingIndicator() {
    guard hasGraphicalSession() else { return }
    let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    if let button = item.button {
        button.image = NSImage(systemSymbolName: "record.circle", accessibilityDescription: "nastro recording")
        button.contentTintColor = .systemRed
        button.toolTip = "nastro - recording"
    }
    recordingStatusItem = item
}

private func hideRecordingIndicator() {
    guard let item = recordingStatusItem else { return }
    NSStatusBar.system.removeStatusItem(item)
    recordingStatusItem = nil
}

// MARK: - Entry point

private func fail(_ message: String, code: Int32 = 1) -> Never {
    FileHandle.standardError.write((message + "\n").data(using: .utf8)!)
    exit(code)
}

// MARK: - Self-check

/// `assert` is compiled out under the Makefile's `-O` build, so the
/// self-check uses this instead: always active regardless of optimization.
private func check(_ condition: Bool, _ message: String) {
    guard !condition else { return }
    FileHandle.standardError.write(("self-check failed: " + message + "\n").data(using: .utf8)!)
    exit(1)
}

private func runSelfCheck() {
    check(snapToStandardRate(43987) == 44100, "snapToStandardRate(43987)")
    check(snapToStandardRate(48000) == 48000, "snapToStandardRate(48000)")
    check(snapToStandardRate(15900) == 16000, "snapToStandardRate(15900)")
    check(snapToStandardRate(30000) == 30000, "snapToStandardRate(30000) should not snap")
    check(snapToStandardRate(0) == 0, "snapToStandardRate(0)")

    check(framesOwed(elapsed: 1, sampleRate: 48000, alreadyEmitted: 0, maxBurst: 999_999) == 48000, "framesOwed normal case")
    check(framesOwed(elapsed: 1, sampleRate: 48000, alreadyEmitted: 48000, maxBurst: 999_999) == 0, "framesOwed zero owed")
    check(framesOwed(elapsed: 1, sampleRate: 48000, alreadyEmitted: 100_000, maxBurst: 999_999) == 0, "framesOwed negative owed clamps to 0")
    check(framesOwed(elapsed: 1, sampleRate: 48000, alreadyEmitted: 0, maxBurst: 1000) == 1000, "framesOwed clamps to maxBurst")
    check(framesOwed(elapsed: 0, sampleRate: 48000, alreadyEmitted: 0, maxBurst: 999_999) == 0, "framesOwed elapsed=0")

    let watcher1 = RateWatcher(assumedRate: 48000)
    check(watcher1.update(frames: 88200, elapsed: 2) == nil, "RateWatcher first deviating window returns nil")
    check(watcher1.update(frames: 88200, elapsed: 2) == 44100, "RateWatcher second consecutive deviating window corrects")

    let watcher2 = RateWatcher(assumedRate: 48000)
    check(watcher2.update(frames: 96000, elapsed: 2) == nil, "RateWatcher matching rate window 1")
    check(watcher2.update(frames: 96000, elapsed: 2) == nil, "RateWatcher matching rate window 2")

    let watcher3 = RateWatcher(assumedRate: 48000)
    check(watcher3.update(frames: 88200, elapsed: 2) == nil, "RateWatcher single deviating window")
    check(watcher3.update(frames: 96000, elapsed: 2) == nil, "RateWatcher direction reset by non-deviating window")

    let watcher4 = RateWatcher(assumedRate: 48000)
    check(watcher4.update(frames: 0, elapsed: 2) == nil, "RateWatcher zero frames ignored")

    // Two deviating windows that disagree with each other are a transition,
    // not a wrong rate, and must not move the assumed rate.
    let watcher5 = RateWatcher(assumedRate: 48000)
    check(watcher5.update(frames: 88200, elapsed: 2) == nil, "RateWatcher disagreeing window 1")
    check(watcher5.update(frames: 64000, elapsed: 2) == nil, "RateWatcher disagreeing window 2")
    check(watcher5.update(frames: 64000, elapsed: 2) == 32000, "RateWatcher corrects once two windows agree")

    FileHandle.standardOutput.write("self-check ok\n".data(using: .utf8)!)
}

if CommandLine.arguments.dropFirst().contains("--self-check") {
    runSelfCheck()
    exit(0)
}

let rawArguments = Array(CommandLine.arguments.dropFirst())
let flags = Set(rawArguments.filter { $0.hasPrefix("--") })
let positional = rawArguments.filter { !$0.hasPrefix("--") }

guard let outputPath = positional.first else {
    fail("Usage: nastro-tap <audio-output-path> [--mic-only|--system-only] [--self-check]")
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

// No dock icon/app switcher entry -- this is a background capture helper,
// not an app -- but the menu bar status item (best-effort, see
// showRecordingIndicator) still needs a policy set for it to be drawable.
NSApplication.shared.setActivationPolicy(.accessory)
showRecordingIndicator()

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

// Periodic metadata.json write, on top of the precise final write in
// shutdown(): caps how much duration a crash/kill -9 can lose to 5s.
let statusTimer = DispatchSource.makeTimerSource(queue: .main)
statusTimer.schedule(deadline: .now() + 5, repeating: 5)
statusTimer.setEventHandler { writeMetadata() }
statusTimer.resume()

/// One stdout line per second: `level sys=<0.00..1.00> mic=<0.00..1.00>`,
/// omitting whichever source isn't active in mic-only/system-only mode. The
/// Go side treats this as a purely additive, best-effort signal -- an older
/// nastro-tap that never prints it just means the TUI's VU meter doesn't
/// appear, nothing else depends on it.
func writeLevelLine(_ line: String) {
    FileHandle.standardOutput.write((line + "\n").data(using: .utf8)!)
}

let levelTimer = DispatchSource.makeTimerSource(queue: .main)
levelTimer.schedule(deadline: .now() + 1, repeating: 1)
levelTimer.setEventHandler {
    recorder.tickRateWatcher()
    let (sys, mic) = recorder.consumeLevels()
    var parts: [String] = []
    if let sys { parts.append(String(format: "sys=%.2f", sys)) }
    if let mic { parts.append(String(format: "mic=%.2f", mic)) }
    guard !parts.isEmpty else { return }
    writeLevelLine("level " + parts.joined(separator: " "))
}
levelTimer.resume()

func shutdown() -> Never {
    hideRecordingIndicator()
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

// NSApplication.shared.run() (not a bare RunLoop.main.run()) is what
// actually drives the status item onto the menu bar -- AppKit needs its own
// event loop pumping, a plain CFRunLoop isn't enough to get it drawn. The
// DispatchSource signal handlers above stay on the main queue/run loop
// either way, so they keep firing under NSApp's loop too.
NSApplication.shared.run()
