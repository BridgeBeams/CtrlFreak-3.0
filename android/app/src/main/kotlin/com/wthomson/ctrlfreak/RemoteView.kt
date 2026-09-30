package com.wthomson.ctrlfreak

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Rect
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.ScaleGestureDetector
import android.view.View
import android.view.ViewConfiguration
import kotlin.math.abs
import kotlin.math.hypot

/**
 * Draws the remote screen (a Bitmap decoded from each JPEG frame) and turns
 * touches into remote input, TeamViewer style:
 *
 *   - tap                  = left click
 *   - double tap           = double click (both clicks sent at the SAME remote
 *                            point, so Windows always registers it even if the
 *                            two taps land a few pixels apart)
 *   - press and hold       = right click
 *   - drag (hold + move)   = left button drag
 *   - two-finger pinch     = zoom
 *   - two-finger drag      = pan when zoomed in, mouse wheel scroll otherwise
 */
class RemoteView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : View(context, attrs) {

    interface Listener {
        fun onLeftClick(x: Int, y: Int)
        fun onRightClick(x: Int, y: Int)
        fun onDragStart(x: Int, y: Int)
        fun onDragMove(x: Int, y: Int)
        fun onDragEnd(x: Int, y: Int)
        fun onScroll(dy: Int)
    }

    var listener: Listener? = null
    var remoteW: Int = 0
    var remoteH: Int = 0

    private var bitmap: Bitmap? = null
    private val dst = Rect()

    // View transform: zoom >= 1, with pan offset from the centered position.
    private var zoom = 1f
    private var panX = 0f
    private var panY = 0f

    // Gesture state.
    private var downVX = 0f
    private var downVY = 0f
    private var lastVX = 0f
    private var lastVY = 0f
    private var moved = false
    private var dragging = false
    private var longPressed = false
    private var multi = false
    private var lastTapTime = 0L
    private var lastTapVX = 0f
    private var lastTapVY = 0f
    private var lastCx = 0f
    private var lastCy = 0f
    private var scrollAnchorY = 0f

    private val slop = ViewConfiguration.get(context).scaledTouchSlop.toFloat()
    private val doubleSlop = 40f * resources.displayMetrics.density
    private val longPress = Runnable {
        if (!moved && !dragging && !multi) {
            longPressed = true
            toRemote(downVX, downVY)?.let { listener?.onRightClick(it.first, it.second) }
        }
    }

    private val scaleDetector = ScaleGestureDetector(context, object : ScaleGestureDetector.SimpleOnScaleGestureListener() {
        override fun onScale(d: ScaleGestureDetector): Boolean {
            val bmp = bitmap ?: return false
            val sw = bmp.width.toFloat(); val sh = bmp.height.toFloat()
            val vw = width.toFloat(); val vh = height.toFloat()
            if (sw <= 0f || sh <= 0f || vw <= 0f || vh <= 0f) return false
            val base = minOf(vw / sw, vh / sh)
            // Keep the point under the fingers fixed as we zoom.
            val iw0 = sw * base * zoom; val ih0 = sh * base * zoom
            val ox0 = (vw - iw0) / 2f + panX; val oy0 = (vh - ih0) / 2f + panY
            val fxFrac = (d.focusX - ox0) / iw0; val fyFrac = (d.focusY - oy0) / ih0
            zoom = (zoom * d.scaleFactor).coerceIn(1f, 5f)
            val iw1 = sw * base * zoom; val ih1 = sh * base * zoom
            panX = d.focusX - fxFrac * iw1 - (vw - iw1) / 2f
            panY = d.focusY - fyFrac * ih1 - (vh - ih1) / 2f
            clampPan()
            invalidate()
            return true
        }
    })

    init {
        setBackgroundColor(Color.BLACK)
        isFocusableInTouchMode = true
    }

    /** Set the current frame. Call on the main thread. */
    fun setFrame(bmp: Bitmap) {
        bitmap = bmp
        if (remoteW == 0) remoteW = bmp.width
        if (remoteH == 0) remoteH = bmp.height
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        val bmp = bitmap ?: return
        val g = geom() ?: return
        dst.set(g[0].toInt(), g[1].toInt(), (g[0] + g[2]).toInt(), (g[1] + g[3]).toInt())
        canvas.drawBitmap(bmp, null, dst, null)
    }

    /** Current image rectangle in view coords: [originX, originY, imgW, imgH]. */
    private fun geom(): FloatArray? {
        val bmp = bitmap ?: return null
        val sw = bmp.width.toFloat(); val sh = bmp.height.toFloat()
        val vw = width.toFloat(); val vh = height.toFloat()
        if (sw <= 0f || sh <= 0f || vw <= 0f || vh <= 0f) return null
        val base = minOf(vw / sw, vh / sh)
        val iw = sw * base * zoom; val ih = sh * base * zoom
        val ox = (vw - iw) / 2f + panX
        val oy = (vh - ih) / 2f + panY
        return floatArrayOf(ox, oy, iw, ih)
    }

    private fun clampPan() {
        val g = geom() ?: return
        val vw = width.toFloat(); val vh = height.toFloat()
        val maxX = maxOf(0f, (g[2] - vw) / 2f)
        val maxY = maxOf(0f, (g[3] - vh) / 2f)
        panX = panX.coerceIn(-maxX, maxX)
        panY = panY.coerceIn(-maxY, maxY)
    }

    private fun toRemote(vx: Float, vy: Float): Pair<Int, Int>? {
        val g = geom() ?: return null
        if (g[2] <= 0f || g[3] <= 0f) return null
        val fx = ((vx - g[0]) / g[2]).coerceIn(0f, 1f)
        val fy = ((vy - g[1]) / g[3]).coerceIn(0f, 1f)
        val rw = if (remoteW > 0) remoteW else 1
        val rh = if (remoteH > 0) remoteH else 1
        return Pair((fx * rw).toInt(), (fy * rh).toInt())
    }

    private fun cx(e: MotionEvent) = (e.getX(0) + e.getX(1)) / 2f
    private fun cy(e: MotionEvent) = (e.getY(0) + e.getY(1)) / 2f

    override fun onTouchEvent(event: MotionEvent): Boolean {
        scaleDetector.onTouchEvent(event)
        val l = listener ?: return false
        when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                downVX = event.x; downVY = event.y; lastVX = event.x; lastVY = event.y
                moved = false; dragging = false; longPressed = false; multi = false
                postDelayed(longPress, 500)
            }
            MotionEvent.ACTION_POINTER_DOWN -> {
                removeCallbacks(longPress)
                if (dragging) { toRemote(lastVX, lastVY)?.let { l.onDragEnd(it.first, it.second) }; dragging = false }
                multi = true
                if (event.pointerCount >= 2) { lastCx = cx(event); lastCy = cy(event); scrollAnchorY = lastCy }
            }
            MotionEvent.ACTION_MOVE -> {
                if (multi) {
                    if (event.pointerCount >= 2 && !scaleDetector.isInProgress) {
                        val ccx = cx(event); val ccy = cy(event)
                        val dx = ccx - lastCx; val dy = ccy - lastCy
                        if (zoom > 1f) {
                            panX += dx; panY += dy; clampPan(); invalidate()
                        } else if (abs(ccy - scrollAnchorY) > 24f) {
                            l.onScroll(if (ccy > scrollAnchorY) -1 else 1); scrollAnchorY = ccy
                        }
                        lastCx = ccx; lastCy = ccy
                    }
                } else {
                    val dx = event.x - downVX; val dy = event.y - downVY
                    if (!dragging && (abs(dx) > slop || abs(dy) > slop)) {
                        moved = true; removeCallbacks(longPress)
                        if (!longPressed) {
                            dragging = true
                            toRemote(downVX, downVY)?.let { l.onDragStart(it.first, it.second) }
                        }
                    }
                    if (dragging) {
                        lastVX = event.x; lastVY = event.y
                        toRemote(event.x, event.y)?.let { l.onDragMove(it.first, it.second) }
                    }
                }
            }
            MotionEvent.ACTION_POINTER_UP -> {
                // Keep multi mode until the last finger lifts, so the finger that
                // stays down does not fire a stray click or drag.
            }
            MotionEvent.ACTION_UP -> {
                removeCallbacks(longPress)
                when {
                    multi -> multi = false
                    dragging -> { toRemote(event.x, event.y)?.let { l.onDragEnd(it.first, it.second) }; dragging = false }
                    longPressed -> longPressed = false
                    else -> {
                        val now = System.currentTimeMillis()
                        val near = hypot((downVX - lastTapVX).toDouble(), (downVY - lastTapVY).toDouble()) < doubleSlop
                        if (now - lastTapTime < 320L && near) {
                            // Second tap of a double-tap: click again at the FIRST
                            // tap's point so both clicks share one pixel.
                            toRemote(lastTapVX, lastTapVY)?.let { l.onLeftClick(it.first, it.second) }
                            lastTapTime = 0L
                        } else {
                            toRemote(downVX, downVY)?.let { l.onLeftClick(it.first, it.second) }
                            lastTapTime = now; lastTapVX = downVX; lastTapVY = downVY
                        }
                    }
                }
            }
            MotionEvent.ACTION_CANCEL -> {
                removeCallbacks(longPress)
                if (dragging) toRemote(lastVX, lastVY)?.let { l.onDragEnd(it.first, it.second) }
                dragging = false; multi = false; longPressed = false
            }
        }
        return true
    }
}
