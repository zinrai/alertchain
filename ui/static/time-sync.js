// Keep Start / Duration / End time inputs consistent.
// When Start or Duration changes, End is recomputed from Start + Duration.
// When End changes, Duration is recomputed from End - Start.
(function () {
  var startEl = document.getElementById('starts-at');
  var durEl = document.getElementById('duration');
  var endEl = document.getElementById('ends-at');
  if (!startEl || !durEl || !endEl) return;

  // Parse a Go-style duration like "1h", "30m", "1h30m", "2d".
  function parseDuration(s) {
    var re = /(\d+)([smhd])/g, total = 0, m;
    while ((m = re.exec(s)) !== null) {
      var n = parseInt(m[1], 10);
      switch (m[2]) {
        case 's': total += n * 1000; break;
        case 'm': total += n * 60000; break;
        case 'h': total += n * 3600000; break;
        case 'd': total += n * 86400000; break;
      }
    }
    return total > 0 ? total : null;
  }

  function formatDuration(ms) {
    var days = Math.floor(ms / 86400000); ms -= days * 86400000;
    var hours = Math.floor(ms / 3600000); ms -= hours * 3600000;
    var minutes = Math.floor(ms / 60000);
    var out = '';
    if (days) out += days + 'd';
    if (hours) out += hours + 'h';
    if (minutes) out += minutes + 'm';
    return out || '0m';
  }

  function pad(n) { return String(n).padStart(2, '0'); }

  // datetime-local input value is interpreted as UTC by alertchain's
  // backend; format as YYYY-MM-DDTHH:MM using UTC components.
  function dateToInput(d) {
    return d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1) + '-' + pad(d.getUTCDate()) +
      'T' + pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes());
  }
  function inputToDate(s) {
    if (!s) return null;
    return new Date(s + 'Z');
  }

  function recomputeEnd() {
    var start = inputToDate(startEl.value);
    var ms = parseDuration(durEl.value);
    if (start && ms) {
      endEl.value = dateToInput(new Date(start.getTime() + ms));
    }
  }

  function recomputeDuration() {
    var start = inputToDate(startEl.value);
    var end = inputToDate(endEl.value);
    if (start && end && end > start) {
      durEl.value = formatDuration(end - start);
    }
  }

  startEl.addEventListener('input', recomputeEnd);
  durEl.addEventListener('input', recomputeEnd);
  endEl.addEventListener('input', recomputeDuration);

  // Matcher row management.
  var addBtn = document.getElementById('add-matcher-row');
  var rows = document.getElementById('matchers');
  if (addBtn && rows) {
    addBtn.addEventListener('click', function () {
      var row = document.createElement('div');
      row.className = 'matcher-row';
      row.innerHTML =
        '<input type="text" name="match-name" placeholder="label">' +
        '<input type="text" name="match-value" placeholder="value">' +
        '<button type="button" class="remove-row">×</button>';
      rows.appendChild(row);
    });
    rows.addEventListener('click', function (e) {
      if (e.target.classList.contains('remove-row')) {
        e.target.parentElement.remove();
      }
    });
  }
})();
