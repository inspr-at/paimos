<script setup lang="ts">
import OfferText from './OfferText.vue'
import { date, type Offer } from './types'
defineProps<{ offer: Offer; editable?: boolean }>()
</script>
<template>
  <div class="offer-cover">
    <div class="top"><div class="word">ANGEBOT</div></div>
    <OfferText
      v-model="offer.document.title"
      tag="h1"
      class="title"
      :editable="editable"
      label="Angebotstitel"
    />
    <OfferText
      v-model="offer.document.subtitle"
      tag="p"
      class="sub"
      :editable="editable"
      label="Untertitel"
    />
    <div class="cols">
      <div class="addr">
        <p class="lbl">Auftraggeber</p>
        <OfferText
          v-model="offer.document.customer.name"
          tag="p"
          class="who"
          :editable="editable"
          label="Firma des Kunden"
        /><OfferText
          v-model="offer.document.customer.address"
          tag="p"
          :editable="editable"
          label="Kundenanschrift"
        />
        <p v-if="offer.document.customer.contact || editable">
          z. Hd.
          <OfferText
            v-model="offer.document.customer.contact"
            :editable="editable"
            label="Kundenkontakt"
          />
        </p>
        <OfferText
          v-model="offer.document.customer.country"
          tag="p"
          :editable="editable"
          label="Land des Kunden"
        />
      </div>
      <dl class="kv">
        <dt>Angebotsnummer</dt>
        <dd>{{ offer.offer_no }}</dd>
        <dt>Angebotsdatum</dt>
        <dd>
          <input
            v-if="editable"
            v-model="offer.document.offer_date"
            type="date"
            aria-label="Angebotsdatum"
          /><template v-else>{{ date(offer.document.offer_date) }}</template>
        </dd>
        <dt>Kundennummer</dt>
        <dd>{{ offer.document.customer.customer_no }}</dd>
        <dt>Gültig bis</dt>
        <dd>
          <input
            v-if="editable"
            v-model="offer.document.valid_until"
            type="date"
            aria-label="Gültig bis"
          /><template v-else>{{ date(offer.document.valid_until) }}</template>
        </dd>
        <dt>Ansprechpartner</dt>
        <dd>
          <OfferText
            v-model="offer.document.sender.contact_person"
            :editable="editable"
            label="Unser Ansprechpartner"
          />
        </dd>
        <template v-if="offer.document.project_ref || editable"
          ><dt>Projektreferenz</dt>
          <dd>
            <OfferText
              v-model="offer.document.project_ref"
              :editable="editable"
              label="Projektreferenz"
            /></dd
        ></template>
      </dl>
    </div>
    <div class="senderline">
      <b>{{ offer.document.sender.company }}</b
      ><span
        >{{ offer.document.sender.street }}, {{ offer.document.sender.postal_code }}
        {{ offer.document.sender.city }}, {{ offer.document.sender.country }}</span
      ><span v-if="offer.document.sender.register_no"
        >{{ offer.document.sender.register_no }} · {{ offer.document.sender.register_court }}</span
      ><span>{{ offer.document.sender.email }}</span
      ><span v-if="offer.document.sender.uid">UID {{ offer.document.sender.uid }}</span>
    </div>
    <div class="intro">
      <OfferText v-model="offer.document.intro" tag="p" :editable="editable" label="Einleitung" />
    </div>
  </div>
</template>
